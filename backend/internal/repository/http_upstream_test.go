package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// HTTPUpstreamSuite HTTP 上游服务测试套件
// 使用 testify/suite 组织测试，支持 SetupTest 初始化
type HTTPUpstreamSuite struct {
	suite.Suite
	cfg *config.Config // 测试用配置
}

// SetupTest 每个测试用例执行前的初始化
// 创建空配置，各测试用例可按需覆盖
func (s *HTTPUpstreamSuite) SetupTest() {
	s.cfg = &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				AllowPrivateHosts: true,
			},
		},
	}
}

// newService 创建测试用的 httpUpstreamService 实例
// 返回具体类型以便访问内部状态进行断言
func (s *HTTPUpstreamSuite) newService() *httpUpstreamService {
	up := NewHTTPUpstream(s.cfg)
	svc, ok := up.(*httpUpstreamService)
	require.True(s.T(), ok, "expected *httpUpstreamService")
	return svc
}

// TestDefaultResponseHeaderTimeout 测试默认响应头超时配置
// 验证未配置时使用 300 秒默认值
func (s *HTTPUpstreamSuite) TestDefaultResponseHeaderTimeout() {
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 300*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestCustomResponseHeaderTimeout 测试自定义响应头超时配置
// 验证配置值能正确应用到 Transport
func (s *HTTPUpstreamSuite) TestCustomResponseHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{ResponseHeaderTimeout: 7}
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 7*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestGetOrCreateClient_InvalidURLReturnsError 测试无效代理 URL 返回错误
// 验证解析失败时拒绝回退到直连模式
func (s *HTTPUpstreamSuite) TestGetOrCreateClient_InvalidURLReturnsError() {
	svc := s.newService()
	_, err := svc.getClientEntry("://bad-proxy-url", 1, 1, false, false)
	require.Error(s.T(), err, "expected error for invalid proxy URL")
}

// TestNormalizeProxyURL_Canonicalizes 测试代理 URL 规范化
// 验证等价地址能够映射到同一缓存键
func (s *HTTPUpstreamSuite) TestNormalizeProxyURL_Canonicalizes() {
	key1, _, err1 := normalizeProxyURL("http://proxy.local:8080")
	require.NoError(s.T(), err1)
	key2, _, err2 := normalizeProxyURL("http://proxy.local:8080/")
	require.NoError(s.T(), err2)
	require.Equal(s.T(), key1, key2, "expected normalized proxy keys to match")
}

// TestAcquireClient_OverLimitReturnsError 测试连接池缓存上限保护
// 验证超限且无可淘汰条目时返回错误
func (s *HTTPUpstreamSuite) TestAcquireClient_OverLimitReturnsError() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy,
		MaxUpstreamClients:      1,
	}
	svc := s.newService()
	entry1, err := svc.acquireClient("http://proxy-a:8080", 1, 1)
	require.NoError(s.T(), err, "expected first acquire to succeed")
	require.NotNil(s.T(), entry1, "expected entry")

	entry2, err := svc.acquireClient("http://proxy-b:8080", 2, 1)
	require.Error(s.T(), err, "expected error when cache limit reached")
	require.Nil(s.T(), entry2, "expected nil entry when cache limit reached")
}

// TestDo_WithoutProxy_GoesDirect 测试无代理时直连
// 验证空代理 URL 时请求直接发送到目标服务器
func (s *HTTPUpstreamSuite) TestDo_WithoutProxy_GoesDirect() {
	// 创建模拟上游服务器
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	s.T().Cleanup(upstream.Close)

	up := NewHTTPUpstream(s.cfg)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/x", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct", string(b), "unexpected body")
}

func (s *HTTPUpstreamSuite) TestDo_RequestErrorPath() {
	svc := s.newService()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/unreachable", nil)
	require.NoError(s.T(), err)

	resp, doErr := svc.Do(req, "", 1, 1)
	require.Nil(s.T(), resp)
	require.Error(s.T(), doErr)
}

// TestDo_WithHTTPProxy_UsesProxy 测试 HTTP 代理功能
// 验证请求通过代理服务器转发，使用绝对 URI 格式
func (s *HTTPUpstreamSuite) TestDo_WithHTTPProxy_UsesProxy() {
	// 用于接收代理请求的通道
	seen := make(chan string, 1)
	// 创建模拟代理服务器
	proxySrv := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.RequestURI // 记录请求 URI
		_, _ = io.WriteString(w, "proxied")
	}))
	s.T().Cleanup(proxySrv.Close)

	s.cfg.Gateway = config.GatewayConfig{ResponseHeaderTimeout: 1}
	up := NewHTTPUpstream(s.cfg)

	// 发送请求到外部地址，应通过代理
	req, err := http.NewRequest(http.MethodGet, "http://example.com/test", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, proxySrv.URL, 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "proxied", string(b), "unexpected body")

	// 验证代理收到的是绝对 URI 格式（HTTP 代理规范要求）
	select {
	case uri := <-seen:
		require.Equal(s.T(), "http://example.com/test", uri, "expected absolute-form request URI")
	default:
		require.Fail(s.T(), "expected proxy to receive request")
	}
}

// TestDo_EmptyProxy_UsesDirect 测试空代理字符串
// 验证空字符串代理等同于直连
func (s *HTTPUpstreamSuite) TestDo_EmptyProxy_UsesDirect() {
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct-empty")
	}))
	s.T().Cleanup(upstream.Close)

	up := NewHTTPUpstream(s.cfg)
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/y", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do with empty proxy")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct-empty", string(b))
}

// TestAccountIsolation_DifferentAccounts 测试账户隔离模式
// 验证不同账户使用独立的连接池
func (s *HTTPUpstreamSuite) TestAccountIsolation_DifferentAccounts() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 同一代理，不同账户
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 2, 3)
	require.NotSame(s.T(), entry1, entry2, "不同账号不应共享连接池")
	require.Equal(s.T(), 2, len(svc.clients), "账号隔离应缓存两个客户端")
}

// TestAccountProxyIsolation_DifferentProxy 测试账户+代理组合隔离模式
// 验证同一账户使用不同代理时创建独立连接池
func (s *HTTPUpstreamSuite) TestAccountProxyIsolation_DifferentProxy() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy}
	svc := s.newService()
	// 同一账户，不同代理
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1, entry2, "账号+代理隔离应区分不同代理")
	require.Equal(s.T(), 2, len(svc.clients), "账号+代理隔离应缓存两个客户端")
}

// TestAccountModeProxyChangeClearsPool 测试账户模式下代理变更
// 验证账户切换代理时清理旧连接池，避免复用错误代理
func (s *HTTPUpstreamSuite) TestAccountModeProxyChangeClearsPool() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 同一账户，先后使用不同代理
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1, entry2, "账号切换代理应创建新连接池")
	require.Equal(s.T(), 1, len(svc.clients), "账号模式下应仅保留一个连接池")
	require.False(s.T(), hasEntry(svc, entry1), "旧连接池应被清理")
}

// TestAccountConcurrencyOverridesPoolSettings 测试账户并发数覆盖连接池配置
// 验证账户隔离模式下，连接池大小与账户并发数对应
func (s *HTTPUpstreamSuite) TestAccountConcurrencyOverridesPoolSettings() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 账户并发数为 12
	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 12)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	// 连接池参数应与并发数一致
	require.Equal(s.T(), 12, transport.MaxConnsPerHost, "MaxConnsPerHost mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConns, "MaxIdleConns mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost mismatch")
}

// TestAccountConcurrencyFallbackToDefault 测试账户并发数为 0 时回退到默认配置
// 验证未指定并发数时使用全局配置值
func (s *HTTPUpstreamSuite) TestAccountConcurrencyFallbackToDefault() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
		MaxIdleConns:            77,
		MaxIdleConnsPerHost:     55,
		MaxConnsPerHost:         66,
	}
	svc := s.newService()
	// 账户并发数为 0，应使用全局配置
	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 66, transport.MaxConnsPerHost, "MaxConnsPerHost fallback mismatch")
	require.Equal(s.T(), 77, transport.MaxIdleConns, "MaxIdleConns fallback mismatch")
	require.Equal(s.T(), 55, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost fallback mismatch")
}

// TestEvictOverLimitRemovesOldestIdle 测试超出数量限制时的 LRU 淘汰
// 验证优先淘汰最久未使用的空闲客户端
func (s *HTTPUpstreamSuite) TestEvictOverLimitRemovesOldestIdle() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy,
		MaxUpstreamClients:      2, // 最多缓存 2 个客户端
	}
	svc := s.newService()
	// 创建两个客户端，设置不同的最后使用时间
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 1)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 2, 1)
	atomic.StoreInt64(&entry1.lastUsed, time.Now().Add(-2*time.Hour).UnixNano()) // 最久
	atomic.StoreInt64(&entry2.lastUsed, time.Now().Add(-time.Hour).UnixNano())
	// 创建第三个客户端，触发淘汰
	_ = mustGetOrCreateClient(s.T(), svc, "http://proxy-c:8080", 3, 1)

	require.LessOrEqual(s.T(), len(svc.clients), 2, "应保持在缓存上限内")
	require.False(s.T(), hasEntry(svc, entry1), "最久未使用的连接池应被清理")
}

// TestIdleTTLDoesNotEvictActive 测试活跃请求保护
// 验证有进行中请求的客户端不会被空闲超时淘汰
func (s *HTTPUpstreamSuite) TestIdleTTLDoesNotEvictActive() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
		ClientIdleTTLSeconds:    1, // 1 秒空闲超时
	}
	svc := s.newService()
	entry1 := mustGetOrCreateClient(s.T(), svc, "", 1, 1)
	// 设置为很久之前使用，但有活跃请求
	atomic.StoreInt64(&entry1.lastUsed, time.Now().Add(-2*time.Minute).UnixNano())
	atomic.StoreInt64(&entry1.inFlight, 1) // 模拟有活跃请求
	// 创建新客户端，触发淘汰检查
	_, _ = svc.getOrCreateClient("", 2, 1)

	require.True(s.T(), hasEntry(svc, entry1), "有活跃请求时不应回收")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfile_UsesHTTP2TransportForHTTPProxy() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    2,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()

	entry, err := svc.getClientEntry("http://proxy.local:8080", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entry.protocolMode)

	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.True(s.T(), transport.ForceAttemptHTTP2, "OpenAI profile should prefer HTTP/2")
	require.Nil(s.T(), transport.TLSNextProto, "HTTP/2 mode should not force-disable TLSNextProto")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfile_FallbackToHTTP11WhenProxyMarkedIncompatible() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    2,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"

	state := svc.getOrCreateOpenAIHTTP2FallbackState(proxyURL)
	state.mu.Lock()
	state.fallbackUntil = time.Now().Add(3 * time.Minute)
	state.mu.Unlock()

	entry, err := svc.getClientEntry(proxyURL, 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entry.protocolMode)

	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2, "fallback mode must disable HTTP/2 force-attempt")
	require.NotNil(s.T(), transport.TLSNextProto, "fallback mode must disable HTTP/2 negotiation")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfile_RecordHTTP2ErrorActivatesFallback() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    2,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	h2Err := errors.New("http2: stream error")

	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL, h2Err)
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(proxyURL), "first error should not activate fallback")

	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL, h2Err)
	require.True(s.T(), svc.isOpenAIHTTP2FallbackActive(proxyURL), "second error in window should activate fallback")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfile_RecordNonHTTP2ErrorDoesNotActivateFallback() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"

	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL, errors.New("dial tcp: i/o timeout"))
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(proxyURL))
}

func (s *HTTPUpstreamSuite) TestDoWithTLS_DisabledDelegatesToDo() {
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	s.T().Cleanup(upstream.Close)

	svc := s.newService()
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/tls-disabled", nil)
	require.NoError(s.T(), err)

	resp, err := svc.DoWithTLS(req, "", 1, 1, false)
	require.NoError(s.T(), err)
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(s.T(), readErr)
	require.Equal(s.T(), "ok", string(body))
}

func (s *HTTPUpstreamSuite) TestDoWithTLS_EnabledHTTPRequestSuccess() {
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "tls-enabled")
	}))
	s.T().Cleanup(upstream.Close)

	svc := s.newService()
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/tls-enabled", nil)
	require.NoError(s.T(), err)

	resp, err := svc.DoWithTLS(req, "", 9, 1, true)
	require.NoError(s.T(), err)
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(resp.Body)
	require.NoError(s.T(), readErr)
	require.Equal(s.T(), "tls-enabled", string(body))
}

func (s *HTTPUpstreamSuite) TestDoWithTLS_EnabledRequestError() {
	svc := s.newService()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/tls-error", nil)
	require.NoError(s.T(), err)

	resp, doErr := svc.DoWithTLS(req, "", 9, 1, true)
	require.Nil(s.T(), resp)
	require.Error(s.T(), doErr)
}

func (s *HTTPUpstreamSuite) TestDoWithTLS_ValidateRequestHostFailure() {
	s.cfg.Security.URLAllowlist.Enabled = true
	s.cfg.Security.URLAllowlist.AllowPrivateHosts = false
	svc := s.newService()

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/test", nil)
	require.NoError(s.T(), err)

	resp, doErr := svc.DoWithTLS(req, "", 1, 1, true)
	require.Nil(s.T(), resp)
	require.Error(s.T(), doErr)
}

func (s *HTTPUpstreamSuite) TestShouldValidateResolvedIPAndValidateRequestHost() {
	svc := s.newService()
	require.False(s.T(), svc.shouldValidateResolvedIP())
	require.NoError(s.T(), svc.validateRequestHost(nil))

	s.cfg.Security.URLAllowlist.Enabled = true
	s.cfg.Security.URLAllowlist.AllowPrivateHosts = false
	require.True(s.T(), svc.shouldValidateResolvedIP())
	require.Error(s.T(), svc.validateRequestHost(nil))

	req, err := http.NewRequest(http.MethodGet, "http:///nohost", nil)
	require.NoError(s.T(), err)
	require.Error(s.T(), svc.validateRequestHost(req))
}

func (s *HTTPUpstreamSuite) TestRedirectCheckerStopsAfterLimit() {
	svc := s.newService()
	req, err := http.NewRequest(http.MethodGet, "http://example.com", nil)
	require.NoError(s.T(), err)

	via := make([]*http.Request, 10)
	require.Error(s.T(), svc.redirectChecker(req, via))
}

func (s *HTTPUpstreamSuite) TestRedirectCheckerValidatesRequestHost() {
	s.cfg.Security.URLAllowlist.Enabled = true
	s.cfg.Security.URLAllowlist.AllowPrivateHosts = false
	svc := s.newService()

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1", nil)
	require.NoError(s.T(), err)
	require.Error(s.T(), svc.redirectChecker(req, nil))
}

func (s *HTTPUpstreamSuite) TestShouldReuseEntryAndEvictBranches() {
	svc := s.newService()
	entry := &upstreamClientEntry{
		proxyKey: "proxy-a",
		poolKey:  "pool-a",
	}
	require.False(s.T(), svc.shouldReuseEntry(nil, config.ConnectionPoolIsolationAccount, "proxy-a", "pool-a"))
	require.False(s.T(), svc.shouldReuseEntry(entry, config.ConnectionPoolIsolationAccount, "proxy-b", "pool-a"))
	require.False(s.T(), svc.shouldReuseEntry(entry, config.ConnectionPoolIsolationProxy, "proxy-a", "pool-b"))
	require.True(s.T(), svc.shouldReuseEntry(entry, config.ConnectionPoolIsolationProxy, "proxy-x", "pool-a"))

	s.cfg.Gateway.MaxUpstreamClients = 2
	svc.clients["k1"] = &upstreamClientEntry{inFlight: 1}
	svc.clients["k2"] = &upstreamClientEntry{inFlight: 1}
	require.False(s.T(), svc.evictOldestIdleLocked())
	require.False(s.T(), svc.evictOverLimitLocked())
}

func (s *HTTPUpstreamSuite) TestBuildCacheKeyAndIsolationMode() {
	svc := s.newService()
	require.Equal(s.T(), "account:1", buildCacheKey(config.ConnectionPoolIsolationAccount, "direct", 1, ""))
	require.Equal(s.T(), "account:2|proxy:px", buildCacheKey(config.ConnectionPoolIsolationAccountProxy, "px", 2, ""))
	require.Equal(s.T(), "proxy:direct", buildCacheKey(config.ConnectionPoolIsolationProxy, "direct", 3, ""))
	require.Equal(s.T(), "account:1|proto:openai_h2", buildCacheKey(config.ConnectionPoolIsolationAccount, "direct", 1, "openai_h2"))

	s.cfg.Gateway.ConnectionPoolIsolation = "invalid"
	require.Equal(s.T(), config.ConnectionPoolIsolationAccountProxy, svc.getIsolationMode())
	s.cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationProxy
	require.Equal(s.T(), config.ConnectionPoolIsolationProxy, svc.getIsolationMode())
}

func (s *HTTPUpstreamSuite) TestResolveProtocolModeAndSettingsBranches() {
	svc := s.newService()
	s.cfg.Gateway.OpenAIHTTP2 = config.GatewayOpenAIHTTP2Config{
		Enabled:                   true,
		AllowProxyFallbackToHTTP1: true,
		FallbackErrorThreshold:    2,
		FallbackWindowSeconds:     60,
		FallbackTTLSeconds:        600,
	}
	parsedHTTPProxy, err := url.Parse("http://proxy.local:8080")
	require.NoError(s.T(), err)
	parsedSOCKSProxy, err := url.Parse("socks5://proxy.local:1080")
	require.NoError(s.T(), err)

	require.Equal(s.T(), upstreamProtocolModeDefault, svc.resolveProtocolMode(service.HTTPUpstreamProfileDefault, "direct", nil))
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, svc.resolveProtocolMode(service.HTTPUpstreamProfileOpenAI, "direct", nil))
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, svc.resolveProtocolMode(service.HTTPUpstreamProfileOpenAI, "socks5://proxy.local:1080", parsedSOCKSProxy))

	state := svc.getOrCreateOpenAIHTTP2FallbackState("http://proxy.local:8080")
	state.mu.Lock()
	state.fallbackUntil = time.Now().Add(10 * time.Second)
	state.mu.Unlock()
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, svc.resolveProtocolMode(service.HTTPUpstreamProfileOpenAI, "http://proxy.local:8080", parsedHTTPProxy))

	s.cfg.Gateway.OpenAIHTTP2.Enabled = false
	require.Equal(s.T(), upstreamProtocolModeDefault, svc.resolveProtocolMode(service.HTTPUpstreamProfileOpenAI, "http://proxy.local:8080", parsedHTTPProxy))
}

func (s *HTTPUpstreamSuite) TestGetClientEntryWithTLS_ReusesAndRebuildsOnProxyChange() {
	s.cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationAccount
	svc := s.newService()
	profile := &tlsfingerprint.Profile{Name: "tls-profile"}

	entry1, err := svc.getClientEntryWithTLS("http://proxy-a.local:8080", 1, 1, profile, false, false)
	require.NoError(s.T(), err)
	entry2, err := svc.getClientEntryWithTLS("http://proxy-a.local:8080", 1, 1, profile, false, false)
	require.NoError(s.T(), err)
	require.Same(s.T(), entry1, entry2)

	entry3, err := svc.getClientEntryWithTLS("http://proxy-b.local:8080", 1, 1, profile, false, false)
	require.NoError(s.T(), err)
	require.NotSame(s.T(), entry1, entry3)
}

func (s *HTTPUpstreamSuite) TestGetClientEntryWithTLS_OverLimitReturnsError() {
	s.cfg.Gateway.ConnectionPoolIsolation = config.ConnectionPoolIsolationAccountProxy
	s.cfg.Gateway.MaxUpstreamClients = 1
	svc := s.newService()
	profile := &tlsfingerprint.Profile{Name: "tls-profile"}

	entry1, err := svc.getClientEntryWithTLS("http://proxy-a.local:8080", 1, 1, profile, true, true)
	require.NoError(s.T(), err)
	require.NotNil(s.T(), entry1)

	entry2, err := svc.getClientEntryWithTLS("http://proxy-b.local:8080", 2, 1, profile, true, true)
	require.ErrorIs(s.T(), err, errUpstreamClientLimitReached)
	require.Nil(s.T(), entry2)
}

func (s *HTTPUpstreamSuite) TestOpenAIFallbackStateHelpers() {
	var state openAIHTTP2FallbackState
	now := time.Now()

	active, until := state.recordFailure(now, 1, time.Minute, time.Minute)
	require.True(s.T(), active)
	require.False(s.T(), until.IsZero())
	require.True(s.T(), state.isFallbackActive(now))
	require.False(s.T(), state.isFallbackActive(now.Add(2*time.Minute)))

	state.recordFailure(now, 3, time.Minute, time.Minute)
	state.recordFailure(now.Add(10*time.Second), 3, time.Minute, time.Minute)
	state.resetErrorWindow()
	require.Equal(s.T(), 0, state.errorCount)
	require.True(s.T(), state.windowStart.IsZero())

	// 在 fallback 活跃期间再次失败，不应重复激活。
	state.fallbackUntil = now.Add(time.Minute)
	activated, _ := state.recordFailure(now.Add(5*time.Second), 1, time.Minute, time.Minute)
	require.False(s.T(), activated)
}

func (s *HTTPUpstreamSuite) TestRecordOpenAIHTTP2SuccessResetsWindow() {
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	state := svc.getOrCreateOpenAIHTTP2FallbackState(proxyURL)
	state.mu.Lock()
	state.errorCount = 5
	state.windowStart = time.Now()
	state.mu.Unlock()

	svc.recordOpenAIHTTP2Success(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL)

	state.mu.Lock()
	defer state.mu.Unlock()
	require.Equal(s.T(), 0, state.errorCount)
	require.True(s.T(), state.windowStart.IsZero())
}

func (s *HTTPUpstreamSuite) TestDo_OpenAIProxySuccessResetsHTTP2ErrorWindow() {
	seen := make(chan struct{}, 1)
	proxySrv := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- struct{}{}:
		default:
		}
		_, _ = io.WriteString(w, "proxied")
	}))
	s.T().Cleanup(proxySrv.Close)

	s.cfg.Gateway.OpenAIHTTP2 = config.GatewayOpenAIHTTP2Config{
		Enabled:                   true,
		AllowProxyFallbackToHTTP1: true,
		FallbackErrorThreshold:    2,
		FallbackWindowSeconds:     60,
		FallbackTTLSeconds:        600,
	}
	svc := s.newService()
	proxyKey, _ := normalizeProxyURL(proxySrv.URL)
	state := svc.getOrCreateOpenAIHTTP2FallbackState(proxyKey)
	state.mu.Lock()
	state.windowStart = time.Now()
	state.errorCount = 3
	state.fallbackUntil = time.Time{}
	state.mu.Unlock()

	req, err := http.NewRequest(http.MethodGet, "http://example.com/reset-window", nil)
	require.NoError(s.T(), err)
	req = req.WithContext(service.WithHTTPUpstreamProfile(context.Background(), service.HTTPUpstreamProfileOpenAI))
	resp, doErr := svc.Do(req, proxySrv.URL, 1, 1)
	require.NoError(s.T(), doErr)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body)

	select {
	case <-seen:
	default:
		require.Fail(s.T(), "expected proxy to receive request")
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	require.Equal(s.T(), 0, state.errorCount)
	require.True(s.T(), state.windowStart.IsZero())
}

func (s *HTTPUpstreamSuite) TestOpenAIFallbackStateMapTypeSafety() {
	svc := s.newService()
	svc.openAIHTTP2Fallbacks.Store("x", "bad-type")
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive("x"))
	state := svc.getOrCreateOpenAIHTTP2FallbackState("x")
	require.NotNil(s.T(), state)
}

func (s *HTTPUpstreamSuite) TestBuildUpstreamTransport_ModeSwitchingAndProxyErrors() {
	settings := defaultPoolSettings(s.cfg)
	parsedProxy, err := url.Parse("http://proxy.local:8080")
	require.NoError(s.T(), err)

	h2Transport, err := buildUpstreamTransport(settings, parsedProxy, upstreamProtocolModeOpenAIH2)
	require.NoError(s.T(), err)
	require.True(s.T(), h2Transport.ForceAttemptHTTP2)

	h1Transport, err := buildUpstreamTransport(settings, parsedProxy, upstreamProtocolModeOpenAIH1Fallback)
	require.NoError(s.T(), err)
	require.False(s.T(), h1Transport.ForceAttemptHTTP2)
	require.NotNil(s.T(), h1Transport.TLSNextProto)

	badProxy, err := url.Parse("ftp://proxy.local:21")
	require.NoError(s.T(), err)
	_, badErr := buildUpstreamTransport(settings, badProxy, upstreamProtocolModeDefault)
	require.Error(s.T(), badErr)
}

func (s *HTTPUpstreamSuite) TestBuildUpstreamTransportWithTLSFingerprintBranches() {
	settings := defaultPoolSettings(s.cfg)
	profile := &tlsfingerprint.Profile{Name: "test-profile"}

	transportDirect, err := buildUpstreamTransportWithTLSFingerprint(settings, nil, profile)
	require.NoError(s.T(), err)
	require.NotNil(s.T(), transportDirect.DialTLSContext)

	httpProxy, err := url.Parse("http://proxy.local:8080")
	require.NoError(s.T(), err)
	transportHTTPProxy, err := buildUpstreamTransportWithTLSFingerprint(settings, httpProxy, profile)
	require.NoError(s.T(), err)
	require.NotNil(s.T(), transportHTTPProxy.DialTLSContext)

	socksProxy, err := url.Parse("socks5://proxy.local:1080")
	require.NoError(s.T(), err)
	transportSOCKSProxy, err := buildUpstreamTransportWithTLSFingerprint(settings, socksProxy, profile)
	require.NoError(s.T(), err)
	require.NotNil(s.T(), transportSOCKSProxy.DialTLSContext)

	unsupportedProxy, err := url.Parse("ftp://proxy.local:21")
	require.NoError(s.T(), err)
	_, unsupportedErr := buildUpstreamTransportWithTLSFingerprint(settings, unsupportedProxy, profile)
	require.Error(s.T(), unsupportedErr)
}

func (s *HTTPUpstreamSuite) TestWrapTrackedBody_NilAndCloseOnce() {
	require.Nil(s.T(), wrapTrackedBody(nil, nil))

	closed := int32(0)
	readCloser := io.NopCloser(strings.NewReader("x"))
	wrapped := wrapTrackedBody(readCloser, func() {
		atomic.AddInt32(&closed, 1)
	})
	require.NotNil(s.T(), wrapped)
	_ = wrapped.Close()
	_ = wrapped.Close()
	require.Equal(s.T(), int32(1), atomic.LoadInt32(&closed))
}

// TestHTTPUpstreamSuite 运行测试套件
func TestHTTPUpstreamSuite(t *testing.T) {
	suite.Run(t, new(HTTPUpstreamSuite))
}

// mustGetOrCreateClient 测试辅助函数，调用 getOrCreateClient 并断言无错误
func mustGetOrCreateClient(t *testing.T, svc *httpUpstreamService, proxyURL string, accountID int64, concurrency int) *upstreamClientEntry {
	t.Helper()
	entry, err := svc.getOrCreateClient(proxyURL, accountID, concurrency)
	require.NoError(t, err, "getOrCreateClient(%q, %d, %d)", proxyURL, accountID, concurrency)
	return entry
}

// hasEntry 检查客户端是否存在于缓存中
// 辅助函数，用于验证淘汰逻辑
func hasEntry(svc *httpUpstreamService, target *upstreamClientEntry) bool {
	for _, entry := range svc.clients {
		if entry == target {
			return true
		}
	}
	return false
}
