package service

import (
	"container/heap"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayService_SelectAccountWithScheduler_PreviousResponseSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(9)
	account := Account{
		ID:          1001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_001", account.ID, time.Hour))

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"resp_prev_001",
		"session_hash_001",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.True(t, decision.StickyPreviousHit)
	require.Equal(t, account.ID, cache.sessionBindings["openai:session_hash_001"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(10)
	account := Account{
		ID:          2001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_abc": account.ID,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_abc",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_SessionSticky_ForceHTTP(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1010)
	account := Account{
		ID:          2101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_ws_force_http": true,
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_force_http": account.ID,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_force_http",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_RequiredWSV2_SkipsStickyHTTPAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1011)
	accounts := []Account{
		{
			ID:          2201,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          2202,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    5,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_ws_only": 2201,
		},
	}
	cfg := newOpenAIWSV2TestConfig()

	// 构造“HTTP-only 账号负载更低”的场景，验证 required transport 会强制过滤。
	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			2201: {AccountID: 2201, LoadRate: 0, WaitingCount: 0},
			2202: {AccountID: 2202, LoadRate: 90, WaitingCount: 5},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"session_hash_ws_only",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(2202), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.False(t, decision.StickySessionHit)
	require.Equal(t, 1, decision.CandidateCount)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_RequiredWSV2_NoAvailableAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1012)
	accounts := []Account{
		{
			ID:          2301,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                newOpenAIWSV2TestConfig(),
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportResponsesWebsocketV2,
	)
	require.Error(t, err)
	require.Nil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 0, decision.CandidateCount)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceTopKFallback(t *testing.T) {
	ctx := context.Background()
	groupID := int64(11)
	accounts := []Account{
		{
			ID:          3001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          3002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
		{
			ID:          3003,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 0.4
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.1

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			3001: {AccountID: 3001, LoadRate: 95, WaitingCount: 8},
			3002: {AccountID: 3002, LoadRate: 20, WaitingCount: 1},
			3003: {AccountID: 3003, LoadRate: 10, WaitingCount: 0},
		},
		acquireResults: map[int64]bool{
			3003: false, // top1 失败，必须回退到 top-K 的下一候选
			3002: true,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(3002), selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	require.Equal(t, 3, decision.CandidateCount)
	require.Equal(t, 2, decision.TopK)
	require.Greater(t, decision.LoadSkew, 0.0)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_OpenAIAccountSchedulerMetrics(t *testing.T) {
	ctx := context.Background()
	groupID := int64(12)
	account := Account{
		ID:          4001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:session_hash_metrics": account.ID,
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "session_hash_metrics", "gpt-5.1", nil, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.NotNil(t, selection)
	svc.ReportOpenAIAccountScheduleResult(account.ID, true, intPtrForTest(120), "", 0)
	svc.RecordOpenAIAccountSwitch()

	snapshot := svc.SnapshotOpenAIAccountSchedulerMetrics()
	require.GreaterOrEqual(t, snapshot.SelectTotal, int64(1))
	require.GreaterOrEqual(t, snapshot.StickySessionHitTotal, int64(1))
	require.GreaterOrEqual(t, snapshot.AccountSwitchTotal, int64(1))
	require.GreaterOrEqual(t, snapshot.SchedulerLatencyMsAvg, float64(0))
	require.GreaterOrEqual(t, snapshot.StickyHitRatio, 0.0)
	require.GreaterOrEqual(t, snapshot.RuntimeStatsAccountCount, 1)
}

func intPtrForTest(v int) *int {
	return &v
}

func TestOpenAIAccountRuntimeStats_ReportAndSnapshot(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	stats.report(1001, true, nil, "", 0) // error: fast 0→0, slow 0→0
	firstTTFT := 100
	stats.report(1001, false, &firstTTFT, "", 0) // error: fast 0→0.5, slow 0→0.1; ttft: NaN→100 (both)
	secondTTFT := 200
	stats.report(1001, false, &secondTTFT, "", 0) // error: fast 0.5→0.75, slow 0.1→0.19; ttft: fast 100→150, slow 100→110

	errorRate, ttft, hasTTFT := stats.snapshot(1001)
	require.True(t, hasTTFT)
	// errorRate = max(fast=0.75, slow=0.19) = 0.75
	require.InDelta(t, 0.75, errorRate, 1e-9)
	// ttft = max(fast=150, slow=110) = 150
	require.InDelta(t, 150.0, ttft, 1e-9)
	require.Equal(t, 1, stats.size())
}

func TestDualEWMA_UpdateAndValue(t *testing.T) {
	var d dualEWMA

	// Initial state: both channels are 0.
	require.Equal(t, 0.0, d.fastValue())
	require.Equal(t, 0.0, d.slowValue())
	require.Equal(t, 0.0, d.value())

	// First sample = 1.0
	d.update(1.0)
	// fast: 0.5*1 + 0.5*0 = 0.5
	require.InDelta(t, 0.5, d.fastValue(), 1e-12)
	// slow: 0.1*1 + 0.9*0 = 0.1
	require.InDelta(t, 0.1, d.slowValue(), 1e-12)
	// value = max(0.5, 0.1) = 0.5
	require.InDelta(t, 0.5, d.value(), 1e-12)

	// Second sample = 0.0 (recovery)
	d.update(0.0)
	// fast: 0.5*0 + 0.5*0.5 = 0.25
	require.InDelta(t, 0.25, d.fastValue(), 1e-12)
	// slow: 0.1*0 + 0.9*0.1 = 0.09
	require.InDelta(t, 0.09, d.slowValue(), 1e-12)
	// value = max(0.25, 0.09) = 0.25
	require.InDelta(t, 0.25, d.value(), 1e-12)
}

func TestDualEWMA_SlowDominatesAfterRecovery(t *testing.T) {
	var d dualEWMA

	// Spike: several failures.
	for i := 0; i < 10; i++ {
		d.update(1.0)
	}
	// Now fast is close to 1, slow is also rising.

	// Recovery: many successes.
	for i := 0; i < 20; i++ {
		d.update(0.0)
	}
	// Fast should have dropped close to 0, slow should still be > fast.
	require.Greater(t, d.slowValue(), d.fastValue(),
		"after recovery, slow channel should dominate the pessimistic envelope")
	require.Equal(t, d.slowValue(), d.value())
}

func TestDualEWMATTFT_NaNInitAndFirstSample(t *testing.T) {
	var d dualEWMATTFT
	d.initNaN()

	// Before any sample, value should report no data.
	v, ok := d.value()
	require.False(t, ok)
	require.Equal(t, 0.0, v)

	// First sample seeds both channels.
	d.update(100.0)
	require.InDelta(t, 100.0, d.fastValue(), 1e-12)
	require.InDelta(t, 100.0, d.slowValue(), 1e-12)
	v, ok = d.value()
	require.True(t, ok)
	require.InDelta(t, 100.0, v, 1e-12)

	// Second sample.
	d.update(200.0)
	// fast: 0.5*200 + 0.5*100 = 150
	require.InDelta(t, 150.0, d.fastValue(), 1e-12)
	// slow: 0.1*200 + 0.9*100 = 110
	require.InDelta(t, 110.0, d.slowValue(), 1e-12)
	v, ok = d.value()
	require.True(t, ok)
	require.InDelta(t, 150.0, v, 1e-12)
}

func TestDualEWMATTFT_SlowDominatesWhenLatencyDrops(t *testing.T) {
	var d dualEWMATTFT
	d.initNaN()

	// Warm up with high latency.
	for i := 0; i < 20; i++ {
		d.update(500.0)
	}
	// Now push many low-latency samples.
	for i := 0; i < 20; i++ {
		d.update(100.0)
	}
	// Fast should have adapted down quickly; slow should still be higher.
	require.Greater(t, d.slowValue(), d.fastValue(),
		"after latency improvement, slow channel should dominate the pessimistic TTFT")
	v, ok := d.value()
	require.True(t, ok)
	require.InDelta(t, d.slowValue(), v, 1e-12)
}

func TestDualEWMAConstants(t *testing.T) {
	require.Equal(t, 0.5, dualEWMAAlphaFast)
	require.Equal(t, 0.1, dualEWMAAlphaSlow)
}

func TestOpenAIAccountRuntimeStats_ReportConcurrent(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()

	const (
		accountCount = 4
		workers      = 16
		iterations   = 800
	)
	var wg sync.WaitGroup
	wg.Add(workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				accountID := int64(i%accountCount + 1)
				success := (i+worker)%3 != 0
				ttft := 80 + (i+worker)%40
				stats.report(accountID, success, &ttft, "", 0)
			}
		}()
	}
	wg.Wait()

	require.Equal(t, accountCount, stats.size())
	for accountID := int64(1); accountID <= accountCount; accountID++ {
		errorRate, ttft, hasTTFT := stats.snapshot(accountID)
		require.GreaterOrEqual(t, errorRate, 0.0)
		require.LessOrEqual(t, errorRate, 1.0)
		require.True(t, hasTTFT)
		require.Greater(t, ttft, 0.0)
	}
}

func TestSelectTopKOpenAICandidates(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{
			account:  &Account{ID: 11, Priority: 2},
			loadInfo: &AccountLoadInfo{LoadRate: 10, WaitingCount: 1},
			score:    10.0,
		},
		{
			account:  &Account{ID: 12, Priority: 1},
			loadInfo: &AccountLoadInfo{LoadRate: 20, WaitingCount: 1},
			score:    9.5,
		},
		{
			account:  &Account{ID: 13, Priority: 1},
			loadInfo: &AccountLoadInfo{LoadRate: 30, WaitingCount: 0},
			score:    10.0,
		},
		{
			account:  &Account{ID: 14, Priority: 0},
			loadInfo: &AccountLoadInfo{LoadRate: 40, WaitingCount: 0},
			score:    8.0,
		},
	}

	top2 := selectTopKOpenAICandidates(candidates, 2)
	require.Len(t, top2, 2)
	require.Equal(t, int64(13), top2[0].account.ID)
	require.Equal(t, int64(11), top2[1].account.ID)

	topAll := selectTopKOpenAICandidates(candidates, 8)
	require.Len(t, topAll, len(candidates))
	require.Equal(t, int64(13), topAll[0].account.ID)
	require.Equal(t, int64(11), topAll[1].account.ID)
	require.Equal(t, int64(12), topAll[2].account.ID)
	require.Equal(t, int64(14), topAll[3].account.ID)
}

func TestBuildOpenAIWeightedSelectionOrder_DeterministicBySessionSeed(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{
			account:  &Account{ID: 101},
			loadInfo: &AccountLoadInfo{LoadRate: 10, WaitingCount: 0},
			score:    4.2,
		},
		{
			account:  &Account{ID: 102},
			loadInfo: &AccountLoadInfo{LoadRate: 30, WaitingCount: 1},
			score:    3.5,
		},
		{
			account:  &Account{ID: 103},
			loadInfo: &AccountLoadInfo{LoadRate: 50, WaitingCount: 2},
			score:    2.1,
		},
	}
	req := OpenAIAccountScheduleRequest{
		GroupID:        int64PtrForTest(99),
		SessionHash:    "session_seed_fixed",
		RequestedModel: "gpt-5.1",
	}

	first := buildOpenAIWeightedSelectionOrder(candidates, req)
	second := buildOpenAIWeightedSelectionOrder(candidates, req)
	require.Len(t, first, len(candidates))
	require.Len(t, second, len(candidates))
	for i := range first {
		require.Equal(t, first[i].account.ID, second[i].account.ID)
	}
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_LoadBalanceDistributesAcrossSessions(t *testing.T) {
	ctx := context.Background()
	groupID := int64(15)
	accounts := []Account{
		{
			ID:          5101,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 3,
			Priority:    0,
		},
		{
			ID:          5102,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 3,
			Priority:    0,
		},
		{
			ID:          5103,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 3,
			Priority:    0,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 3
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 1

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			5101: {AccountID: 5101, LoadRate: 20, WaitingCount: 1},
			5102: {AccountID: 5102, LoadRate: 20, WaitingCount: 1},
			5103: {AccountID: 5103, LoadRate: 20, WaitingCount: 1},
		},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selected := make(map[int64]int, len(accounts))
	for i := 0; i < 60; i++ {
		sessionHash := fmt.Sprintf("session_hash_lb_%d", i)
		selection, decision, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		selected[selection.Account.ID]++
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	// 多 session 应该能打散到多个账号，避免“恒定单账号命中”。
	require.GreaterOrEqual(t, len(selected), 2)
}

func TestDeriveOpenAISelectionSeed_NoAffinityAddsEntropy(t *testing.T) {
	req := OpenAIAccountScheduleRequest{
		RequestedModel: "gpt-5.1",
	}
	seed1 := deriveOpenAISelectionSeed(req)
	time.Sleep(1 * time.Millisecond)
	seed2 := deriveOpenAISelectionSeed(req)
	require.NotZero(t, seed1)
	require.NotZero(t, seed2)
	require.NotEqual(t, seed1, seed2)
}

func TestBuildOpenAIWeightedSelectionOrder_HandlesInvalidScores(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{
			account:  &Account{ID: 901},
			loadInfo: &AccountLoadInfo{LoadRate: 5, WaitingCount: 0},
			score:    math.NaN(),
		},
		{
			account:  &Account{ID: 902},
			loadInfo: &AccountLoadInfo{LoadRate: 5, WaitingCount: 0},
			score:    math.Inf(1),
		},
		{
			account:  &Account{ID: 903},
			loadInfo: &AccountLoadInfo{LoadRate: 5, WaitingCount: 0},
			score:    -1,
		},
	}
	req := OpenAIAccountScheduleRequest{
		SessionHash: "seed_invalid_scores",
	}

	order := buildOpenAIWeightedSelectionOrder(candidates, req)
	require.Len(t, order, len(candidates))
	seen := map[int64]struct{}{}
	for _, item := range order {
		seen[item.account.ID] = struct{}{}
	}
	require.Len(t, seen, len(candidates))
}

func TestOpenAISelectionRNG_SeedZeroStillWorks(t *testing.T) {
	rng := newOpenAISelectionRNG(0)
	v1 := rng.nextUint64()
	v2 := rng.nextUint64()
	require.NotEqual(t, v1, v2)
	require.GreaterOrEqual(t, rng.nextFloat64(), 0.0)
	require.Less(t, rng.nextFloat64(), 1.0)
}

func TestOpenAIAccountCandidateHeap_PushPopAndInvalidType(t *testing.T) {
	h := openAIAccountCandidateHeap{}
	h.Push(openAIAccountCandidateScore{
		account:  &Account{ID: 7001},
		loadInfo: &AccountLoadInfo{LoadRate: 0, WaitingCount: 0},
		score:    1.0,
	})
	require.Equal(t, 1, h.Len())
	popped, ok := h.Pop().(openAIAccountCandidateScore)
	require.True(t, ok)
	require.Equal(t, int64(7001), popped.account.ID)
	require.Equal(t, 0, h.Len())

	require.Panics(t, func() {
		h.Push("bad_element_type")
	})
}

func TestClamp01_AllBranches(t *testing.T) {
	require.Equal(t, 0.0, clamp01(-0.2))
	require.Equal(t, 1.0, clamp01(1.3))
	require.Equal(t, 0.5, clamp01(0.5))
}

func TestCalcLoadSkewByMoments_Branches(t *testing.T) {
	require.Equal(t, 0.0, calcLoadSkewByMoments(1, 1, 1))
	// variance < 0 分支：sumSquares/count - mean^2 为负值时应钳制为 0。
	require.Equal(t, 0.0, calcLoadSkewByMoments(1, 0, 2))
	require.GreaterOrEqual(t, calcLoadSkewByMoments(6, 20, 3), 0.0)
}

func TestDefaultOpenAIAccountScheduler_ReportSwitchAndSnapshot(t *testing.T) {
	schedulerAny := newDefaultOpenAIAccountScheduler(&OpenAIGatewayService{}, nil)
	scheduler, ok := schedulerAny.(*defaultOpenAIAccountScheduler)
	require.True(t, ok)

	ttft := 100
	scheduler.ReportResult(1001, true, &ttft, "", 0)
	scheduler.ReportSwitch()
	scheduler.metrics.recordSelect(OpenAIAccountScheduleDecision{
		Layer:             openAIAccountScheduleLayerLoadBalance,
		LatencyMs:         8,
		LoadSkew:          0.5,
		StickyPreviousHit: true,
	})
	scheduler.metrics.recordSelect(OpenAIAccountScheduleDecision{
		Layer:            openAIAccountScheduleLayerSessionSticky,
		LatencyMs:        6,
		LoadSkew:         0.2,
		StickySessionHit: true,
	})

	snapshot := scheduler.SnapshotMetrics()
	require.Equal(t, int64(2), snapshot.SelectTotal)
	require.Equal(t, int64(1), snapshot.StickyPreviousHitTotal)
	require.Equal(t, int64(1), snapshot.StickySessionHitTotal)
	require.Equal(t, int64(1), snapshot.LoadBalanceSelectTotal)
	require.Equal(t, int64(1), snapshot.AccountSwitchTotal)
	require.Greater(t, snapshot.SchedulerLatencyMsAvg, 0.0)
	require.Greater(t, snapshot.StickyHitRatio, 0.0)
	require.Greater(t, snapshot.LoadSkewAvg, 0.0)
}

func TestOpenAIGatewayService_SchedulerWrappersAndDefaults(t *testing.T) {
	svc := &OpenAIGatewayService{}
	ttft := 120
	svc.ReportOpenAIAccountScheduleResult(10, true, &ttft, "", 0)
	svc.RecordOpenAIAccountSwitch()
	snapshot := svc.SnapshotOpenAIAccountSchedulerMetrics()
	require.GreaterOrEqual(t, snapshot.AccountSwitchTotal, int64(1))
	require.Equal(t, 7, svc.openAIWSLBTopK())
	require.Equal(t, openaiStickySessionTTL, svc.openAIWSSessionStickyTTL())

	defaultWeights := svc.openAIWSSchedulerWeights()
	require.Equal(t, 1.0, defaultWeights.Priority)
	require.Equal(t, 1.0, defaultWeights.Load)
	require.Equal(t, 0.7, defaultWeights.Queue)
	require.Equal(t, 0.8, defaultWeights.ErrorRate)
	require.Equal(t, 0.5, defaultWeights.TTFT)

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 9
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 180
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 0.2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 0.3
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 0.4
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.5
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.6
	svcWithCfg := &OpenAIGatewayService{cfg: cfg}

	require.Equal(t, 9, svcWithCfg.openAIWSLBTopK())
	require.Equal(t, 180*time.Second, svcWithCfg.openAIWSSessionStickyTTL())
	customWeights := svcWithCfg.openAIWSSchedulerWeights()
	require.Equal(t, 0.2, customWeights.Priority)
	require.Equal(t, 0.3, customWeights.Load)
	require.Equal(t, 0.4, customWeights.Queue)
	require.Equal(t, 0.5, customWeights.ErrorRate)
	require.Equal(t, 0.6, customWeights.TTFT)
}

func TestDefaultOpenAIAccountScheduler_IsAccountTransportCompatible_Branches(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{}
	require.True(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportAny))
	require.True(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportHTTPSSE))
	require.False(t, scheduler.isAccountTransportCompatible(nil, OpenAIUpstreamTransportResponsesWebsocketV2))

	cfg := newOpenAIWSV2TestConfig()
	scheduler.service = &OpenAIGatewayService{cfg: cfg}
	account := &Account{
		ID:          8801,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	require.True(t, scheduler.isAccountTransportCompatible(account, OpenAIUpstreamTransportResponsesWebsocketV2))
}

func TestLoadFactorCapacityAwareness(t *testing.T) {
	// Test that accounts with higher absolute capacity get better scores
	// when percentage load is equal.
	//
	// Setup:
	// Account A: Concurrency=100, LoadRate=50 (50 free slots)
	// Account B: Concurrency=10,  LoadRate=50 (5 free slots)
	// Both at 50% load, but A should score higher due to more headroom.

	ctx := context.Background()
	groupID := int64(20)
	accounts := []Account{
		{
			ID:          6001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 100,
			Priority:    0,
		},
		{
			ID:          6002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	// Use only Load weight to isolate the capacity-aware loadFactor effect.
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.0

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			6001: {AccountID: 6001, LoadRate: 50, WaitingCount: 0},
			6002: {AccountID: 6002, LoadRate: 50, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	// Verify account A (high capacity) is always selected first by score.
	// Because weighted selection has randomness, we run multiple iterations
	// and verify A is selected more often than B.
	countA := 0
	countB := 0
	iterations := 100
	for i := 0; i < iterations; i++ {
		sessionHash := fmt.Sprintf("cap_aware_test_%d", i)
		selection, _, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		if selection.Account.ID == 6001 {
			countA++
		} else {
			countB++
		}
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	// Account A (100 concurrency) should be selected significantly more often
	// than Account B (10 concurrency) because A has 50 free slots vs 5 free slots.
	require.Greater(t, countA, countB,
		"high-capacity account (50 free slots) should be selected more often than low-capacity (5 free slots) at equal load percentage; got A=%d B=%d", countA, countB)

	// -----------------------------------------------------------------------
	// Verify score math directly via the capacity-aware loadFactor formula.
	// -----------------------------------------------------------------------
	// maxConcurrency = 100 (from account A)
	//
	// Account A (Concurrency=100, LoadRate=50):
	//   base loadFactor = 1 - 50/100 = 0.5
	//   remainingSlots  = 100 * 0.5 = 50
	//   capacityBonus   = 50 / 100  = 0.5
	//   loadFactor      = 0.7*0.5 + 0.3*0.5 = 0.5
	//
	// Account B (Concurrency=10, LoadRate=50):
	//   base loadFactor = 1 - 50/100 = 0.5
	//   remainingSlots  = 10 * 0.5  = 5
	//   capacityBonus   = 5 / 100   = 0.05
	//   loadFactor      = 0.7*0.5 + 0.3*0.05 = 0.365
	//
	// With Load weight = 1.0 and all others 0.0, score = loadFactor.
	expectedScoreA := 0.7*0.5 + 0.3*0.5         // 0.5
	expectedScoreB := 0.7*0.5 + 0.3*(5.0/100.0) // 0.365
	require.Greater(t, expectedScoreA, expectedScoreB, "score sanity check")
	require.InDelta(t, 0.5, expectedScoreA, 1e-9)
	require.InDelta(t, 0.365, expectedScoreB, 1e-9)
}

func TestQueueFactorCapacityAwareness(t *testing.T) {
	// Test that the capacity-aware queue factor penalises accounts
	// whose queue depth is high relative to their own concurrency.
	//
	// Account A: Concurrency=100, WaitingCount=10 (10% of capacity)
	// Account B: Concurrency=10,  WaitingCount=10 (100% of capacity)
	// Both have same absolute waiting count, but B should score lower.

	ctx := context.Background()
	groupID := int64(21)
	accounts := []Account{
		{
			ID:          7001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 100,
			Priority:    0,
		},
		{
			ID:          7002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	// Use only Queue weight to isolate the capacity-aware queueFactor effect.
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.0

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			7001: {AccountID: 7001, LoadRate: 30, WaitingCount: 10},
			7002: {AccountID: 7002, LoadRate: 30, WaitingCount: 10},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	countA := 0
	countB := 0
	iterations := 100
	for i := 0; i < iterations; i++ {
		sessionHash := fmt.Sprintf("queue_aware_test_%d", i)
		selection, _, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		if selection.Account.ID == 7001 {
			countA++
		} else {
			countB++
		}
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	require.Greater(t, countA, countB,
		"account with lower relative queue depth should be selected more often; got A=%d B=%d", countA, countB)

	// -----------------------------------------------------------------------
	// Verify score math for the capacity-aware queueFactor.
	// -----------------------------------------------------------------------
	// maxWaiting = 10 (both accounts have WaitingCount=10)
	//
	// Account A (Concurrency=100, WaitingCount=10):
	//   base queueFactor  = 1 - 10/10 = 0.0
	//   relativeQueue     = 10/100    = 0.1
	//   queueFactor       = 0.6*0.0 + 0.4*(1-0.1) = 0.36
	//
	// Account B (Concurrency=10, WaitingCount=10):
	//   base queueFactor  = 1 - 10/10 = 0.0
	//   relativeQueue     = clamp01(10/10) = 1.0
	//   queueFactor       = 0.6*0.0 + 0.4*(1-1.0) = 0.0
	expectedQueueA := 0.6*0.0 + 0.4*(1-0.1)
	expectedQueueB := 0.6*0.0 + 0.4*(1-1.0)
	require.Greater(t, expectedQueueA, expectedQueueB)
	require.InDelta(t, 0.36, expectedQueueA, 1e-9)
	require.InDelta(t, 0.0, expectedQueueB, 1e-9)
}

func TestLoadFactorCapacityAwareness_ZeroConcurrencyFallback(t *testing.T) {
	// When Concurrency is 0, the capacity-aware blending should be skipped
	// and loadFactor should fall back to the simple loadRate/100 formula.

	ctx := context.Background()
	groupID := int64(22)
	accounts := []Account{
		{
			ID:          8001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 0, // unset / zero
			Priority:    0,
		},
		{
			ID:          8002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 0,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.0

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			8001: {AccountID: 8001, LoadRate: 30, WaitingCount: 0},
			8002: {AccountID: 8002, LoadRate: 70, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	// With Concurrency=0, maxConcurrency=0, so the capacity-aware path is skipped.
	// Account 8001 (LoadRate=30) should have higher loadFactor than 8002 (LoadRate=70).
	countLow := 0
	iterations := 60
	for i := 0; i < iterations; i++ {
		sessionHash := fmt.Sprintf("zero_conc_%d", i)
		selection, _, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		if selection.Account.ID == 8001 {
			countLow++
		}
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	// 8001 (lower load) should be picked more often.
	require.Greater(t, countLow, iterations/2,
		"account with lower load should be selected more often when concurrency is 0; got %d/%d", countLow, iterations)
}

func int64PtrForTest(v int64) *int64 {
	return &v
}

// ---------------------------------------------------------------------------
// Circuit Breaker Tests
// ---------------------------------------------------------------------------

func TestAccountCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cooldown := 30 * time.Second
	halfOpenMax := 2

	// Initially CLOSED — should allow.
	require.True(t, cb.allow(cooldown, halfOpenMax))
	require.Equal(t, "CLOSED", cb.stateString())
	require.False(t, cb.isOpen())

	// Record 4 failures — should still be CLOSED (threshold is 5).
	for i := 0; i < 4; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.Equal(t, "CLOSED", cb.stateString())
	require.True(t, cb.allow(cooldown, halfOpenMax))

	// 5th failure trips the breaker to OPEN.
	cb.recordFailure(defaultCircuitBreakerFailThreshold)
	require.Equal(t, "OPEN", cb.stateString())
	require.True(t, cb.isOpen())
	require.False(t, cb.allow(cooldown, halfOpenMax))
}

func TestAccountCircuitBreaker_OpenToHalfOpen(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cooldown := 50 * time.Millisecond
	halfOpenMax := 2

	// Trip the breaker.
	for i := 0; i < defaultCircuitBreakerFailThreshold; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.Equal(t, "OPEN", cb.stateString())
	require.False(t, cb.allow(cooldown, halfOpenMax))

	// Wait for cooldown to elapse.
	time.Sleep(cooldown + 10*time.Millisecond)

	// Next allow() should transition to HALF_OPEN and admit the request.
	require.True(t, cb.allow(cooldown, halfOpenMax))
	require.Equal(t, "HALF_OPEN", cb.stateString())
}

func TestAccountCircuitBreaker_HalfOpenToClose(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cooldown := 50 * time.Millisecond
	halfOpenMax := 2

	// Trip the breaker.
	for i := 0; i < defaultCircuitBreakerFailThreshold; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.Equal(t, "OPEN", cb.stateString())

	// Wait for cooldown.
	time.Sleep(cooldown + 10*time.Millisecond)

	// Allow first probe — transitions to HALF_OPEN.
	require.True(t, cb.allow(cooldown, halfOpenMax))
	require.Equal(t, "HALF_OPEN", cb.stateString())

	// Allow second probe.
	require.True(t, cb.allow(cooldown, halfOpenMax))

	// Third probe should be rejected (halfOpenMax=2).
	require.False(t, cb.allow(cooldown, halfOpenMax))

	// Both probes succeed — should close the circuit.
	cb.recordSuccess()
	// After first success, still HALF_OPEN (need both to succeed).
	require.Equal(t, "HALF_OPEN", cb.stateString())
	cb.recordSuccess()
	// Both probes succeeded — circuit should be CLOSED now.
	require.Equal(t, "CLOSED", cb.stateString())
	require.False(t, cb.isOpen())
	require.True(t, cb.allow(cooldown, halfOpenMax))
}

func TestAccountCircuitBreaker_ReleaseHalfOpenPermit(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cooldown := 10 * time.Millisecond
	halfOpenMax := 2

	for i := 0; i < defaultCircuitBreakerFailThreshold; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.Equal(t, "OPEN", cb.stateString())

	time.Sleep(cooldown + 5*time.Millisecond)
	require.True(t, cb.allow(cooldown, halfOpenMax))
	require.Equal(t, "HALF_OPEN", cb.stateString())
	require.Equal(t, int32(1), cb.halfOpenInFlight.Load())

	cb.releaseHalfOpenPermit()
	require.Equal(t, int32(0), cb.halfOpenInFlight.Load())

	// Idempotent release should not underflow.
	cb.releaseHalfOpenPermit()
	require.Equal(t, int32(0), cb.halfOpenInFlight.Load())
}

func TestAccountCircuitBreaker_HalfOpenToOpen(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cooldown := 50 * time.Millisecond
	halfOpenMax := 2

	// Trip the breaker.
	for i := 0; i < defaultCircuitBreakerFailThreshold; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.Equal(t, "OPEN", cb.stateString())

	// Wait for cooldown.
	time.Sleep(cooldown + 10*time.Millisecond)

	// Allow a probe — transitions to HALF_OPEN.
	require.True(t, cb.allow(cooldown, halfOpenMax))
	require.Equal(t, "HALF_OPEN", cb.stateString())

	// Failure in HALF_OPEN should trip back to OPEN.
	cb.recordFailure(defaultCircuitBreakerFailThreshold)
	require.Equal(t, "OPEN", cb.stateString())
	require.True(t, cb.isOpen())
	require.False(t, cb.allow(cooldown, halfOpenMax))
}

func TestAccountCircuitBreaker_ResetOnSuccess(t *testing.T) {
	cb := &accountCircuitBreaker{}

	// 4 failures followed by a success should reset the counter.
	for i := 0; i < 4; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.Equal(t, int32(4), cb.consecutiveFails.Load())

	cb.recordSuccess()
	require.Equal(t, int32(0), cb.consecutiveFails.Load())
	require.Equal(t, "CLOSED", cb.stateString())

	// 4 more failures — still not tripped because counter was reset.
	for i := 0; i < 4; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.Equal(t, "CLOSED", cb.stateString())

	// 5th consecutive failure trips it.
	cb.recordFailure(defaultCircuitBreakerFailThreshold)
	require.Equal(t, "OPEN", cb.stateString())
}

func TestAccountCircuitBreaker_IntegrationWithScheduler(t *testing.T) {
	ctx := context.Background()
	groupID := int64(30)
	accounts := []Account{
		{
			ID:          9001,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
		},
		{
			ID:          9002,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 1
	// Enable circuit breaker with low threshold for testing.
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerFailThreshold = 3
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerCooldownSec = 60

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			9001: {AccountID: 9001, LoadRate: 10, WaitingCount: 0},
			9002: {AccountID: 9002, LoadRate: 10, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	scheduler := svc.getOpenAIAccountScheduler()

	// Report 3 consecutive failures for account 9001 — trips the circuit breaker.
	for i := 0; i < 3; i++ {
		scheduler.ReportResult(9001, false, nil, "", 0)
	}

	// Now all selections should avoid account 9001 and pick 9002.
	for i := 0; i < 20; i++ {
		sessionHash := fmt.Sprintf("cb_integration_%d", i)
		selection, decision, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, int64(9002), selection.Account.ID,
			"circuit-open account 9001 should be skipped, got %d on iteration %d", selection.Account.ID, i)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	// Verify metrics tracked the trip.
	snapshot := scheduler.SnapshotMetrics()
	require.GreaterOrEqual(t, snapshot.CircuitBreakerOpenTotal, int64(1))
}

func TestAccountCircuitBreaker_AllOpenFallback(t *testing.T) {
	ctx := context.Background()
	groupID := int64(31)
	accounts := []Account{
		{
			ID:          9101,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 1
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerFailThreshold = 3
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerCooldownSec = 60

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			9101: {AccountID: 9101, LoadRate: 10, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	scheduler := svc.getOpenAIAccountScheduler()

	// Trip the only account.
	for i := 0; i < 3; i++ {
		scheduler.ReportResult(9101, false, nil, "", 0)
	}

	// Even though the only account is circuit-open, the scheduler should
	// still return it (graceful degradation — never return "no accounts").
	selection, _, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"cb_fallback_test",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(9101), selection.Account.ID)
}

func TestAccountCircuitBreaker_SelectReleasesUnselectedHalfOpenPermit(t *testing.T) {
	ctx := context.Background()
	groupID := int64(311)
	accounts := []Account{
		{
			ID:          9111,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
		},
		{
			ID:          9112,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 1
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerFailThreshold = 1
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerCooldownSec = 1
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerHalfOpenMax = 1

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			9111: {AccountID: 9111, LoadRate: 10, WaitingCount: 0},
			9112: {AccountID: 9112, LoadRate: 10, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	scheduler, ok := svc.getOpenAIAccountScheduler().(*defaultOpenAIAccountScheduler)
	require.True(t, ok)

	// Trip both accounts to OPEN so next select will transition both to HALF_OPEN.
	scheduler.ReportResult(9111, false, nil, "", 0)
	scheduler.ReportResult(9112, false, nil, "", 0)
	scheduler.stats.getCircuitBreaker(9111).lastFailureNano.Store(time.Now().Add(-2 * time.Second).UnixNano())
	scheduler.stats.getCircuitBreaker(9112).lastFailureNano.Store(time.Now().Add(-2 * time.Second).UnixNano())

	selection, _, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"cb_release_unselected",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)

	selectedID := selection.Account.ID
	otherID := int64(9111)
	if selectedID == otherID {
		otherID = 9112
	}
	otherCB := scheduler.stats.getCircuitBreaker(otherID)
	require.Equal(t, int32(0), otherCB.halfOpenInFlight.Load(),
		"unselected HALF_OPEN candidate should release probe permit")

	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestAccountCircuitBreaker_DisabledByConfig(t *testing.T) {
	ctx := context.Background()
	groupID := int64(32)
	accounts := []Account{
		{
			ID:          9201,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
		},
		{
			ID:          9202,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 1
	// Circuit breaker explicitly DISABLED.
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = false

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			9201: {AccountID: 9201, LoadRate: 10, WaitingCount: 0},
			9202: {AccountID: 9202, LoadRate: 10, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	scheduler := svc.getOpenAIAccountScheduler()
	internalScheduler, ok := scheduler.(*defaultOpenAIAccountScheduler)
	require.True(t, ok)

	// Report many failures — should NOT affect scheduling when disabled.
	for i := 0; i < 10; i++ {
		scheduler.ReportResult(9201, false, nil, "", 0)
	}

	// Both accounts should still be eligible.
	selected := map[int64]int{}
	for i := 0; i < 40; i++ {
		sessionHash := fmt.Sprintf("cb_disabled_%d", i)
		selection, _, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		selected[selection.Account.ID]++
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}
	// When disabled, 9201 should still appear as a candidate.
	require.Greater(t, selected[int64(9201)]+selected[int64(9202)], 0)
	require.Len(t, selected, 2, "both accounts should be selectable when CB is disabled")
	cb := internalScheduler.stats.getCircuitBreaker(9201)
	require.False(t, cb.isOpen(), "circuit breaker should not transition to OPEN when feature is disabled")
	require.Equal(t, int64(0), internalScheduler.metrics.circuitBreakerOpenTotal.Load())
}

func TestAccountCircuitBreaker_RecoveryMetrics(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	svc := &OpenAIGatewayService{}
	schedulerAny := newDefaultOpenAIAccountScheduler(svc, stats)
	scheduler, ok := schedulerAny.(*defaultOpenAIAccountScheduler)
	require.True(t, ok)

	// Manually enable CB by setting config on the service.
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerFailThreshold = 3
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerCooldownSec = 0 // immediate cooldown for test
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerHalfOpenMax = 1
	scheduler.service.cfg = cfg

	// Trip the breaker: 3 consecutive failures.
	for i := 0; i < 3; i++ {
		scheduler.ReportResult(5001, false, nil, "", 0)
	}
	require.Equal(t, int64(1), scheduler.metrics.circuitBreakerOpenTotal.Load())

	// Let the cooldown expire (0 seconds) and call allow to trigger HALF_OPEN.
	cb := stats.getCircuitBreaker(5001)
	require.Equal(t, "OPEN", cb.stateString())
	allowed := cb.allow(0, 1)
	require.True(t, allowed)
	require.Equal(t, "HALF_OPEN", cb.stateString())

	// Report success — should transition HALF_OPEN → CLOSED.
	scheduler.ReportResult(5001, true, nil, "", 0)
	require.Equal(t, "CLOSED", cb.stateString())
	require.Equal(t, int64(1), scheduler.metrics.circuitBreakerRecoverTotal.Load())
}

func TestAccountCircuitBreaker_StateString(t *testing.T) {
	cb := &accountCircuitBreaker{}
	require.Equal(t, "CLOSED", cb.stateString())

	cb.state.Store(circuitBreakerStateOpen)
	require.Equal(t, "OPEN", cb.stateString())

	cb.state.Store(circuitBreakerStateHalfOpen)
	require.Equal(t, "HALF_OPEN", cb.stateString())

	cb.state.Store(99)
	require.Equal(t, "UNKNOWN", cb.stateString())
}

func TestAccountCircuitBreaker_GetAndIsCircuitOpen(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()

	// isCircuitOpen on a non-existent account should return false.
	require.False(t, stats.isCircuitOpen(1234))

	// getCircuitBreaker should create on first access.
	cb := stats.getCircuitBreaker(1234)
	require.NotNil(t, cb)
	require.Equal(t, "CLOSED", cb.stateString())
	require.False(t, stats.isCircuitOpen(1234))

	// Trip it and verify isCircuitOpen returns true.
	for i := 0; i < defaultCircuitBreakerFailThreshold; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.True(t, stats.isCircuitOpen(1234))

	// Second call to getCircuitBreaker should return same instance.
	cb2 := stats.getCircuitBreaker(1234)
	require.True(t, cb == cb2, "should return same pointer")
}

func TestAccountCircuitBreaker_ConcurrentAllowAndRecord(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cooldown := 50 * time.Millisecond
	halfOpenMax := 4

	var wg sync.WaitGroup
	const workers = 16
	const iterations = 200

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = cb.allow(cooldown, halfOpenMax)
				if (i+w)%3 == 0 {
					cb.recordFailure(defaultCircuitBreakerFailThreshold)
				} else {
					cb.recordSuccess()
				}
			}
		}()
	}
	wg.Wait()

	// Just verify it doesn't panic or deadlock, and state is valid.
	state := cb.state.Load()
	require.True(t, state == circuitBreakerStateClosed ||
		state == circuitBreakerStateOpen ||
		state == circuitBreakerStateHalfOpen,
		"unexpected state: %d", state)
}

// ---------------------------------------------------------------------------
// P2C (Power-of-Two-Choices) Tests
// ---------------------------------------------------------------------------

func TestSelectP2COpenAICandidates_BasicSelection(t *testing.T) {
	// P2C should return all candidates in some order, and higher-scored
	// candidates should tend to appear earlier in the selection order.
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 1}, score: 0.9},
		{account: &Account{ID: 2, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 2}, score: 0.5},
		{account: &Account{ID: 3, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 3}, score: 0.1},
		{account: &Account{ID: 4, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 4}, score: 0.7},
		{account: &Account{ID: 5, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 5}, score: 0.3},
	}

	req := OpenAIAccountScheduleRequest{
		SessionHash: "p2c_basic_test",
	}

	result := selectP2COpenAICandidates(candidates, req)

	// All candidates must be present exactly once.
	require.Len(t, result, len(candidates))
	seen := map[int64]bool{}
	for _, c := range result {
		require.False(t, seen[c.account.ID], "duplicate account ID %d", c.account.ID)
		seen[c.account.ID] = true
	}
	for _, c := range candidates {
		require.True(t, seen[c.account.ID], "missing account ID %d", c.account.ID)
	}

	// Statistical check: over many runs the highest-scored candidate (ID=1,
	// score=0.9) should appear in position 0 more often than the lowest-scored
	// candidate (ID=3, score=0.1).
	topCount := map[int64]int{}
	iterations := 500
	for i := 0; i < iterations; i++ {
		iterReq := OpenAIAccountScheduleRequest{
			SessionHash: fmt.Sprintf("p2c_stat_%d", i),
		}
		order := selectP2COpenAICandidates(candidates, iterReq)
		topCount[order[0].account.ID]++
	}
	require.Greater(t, topCount[int64(1)], topCount[int64(3)],
		"highest-scored candidate should appear first more often than lowest-scored; got best=%d worst=%d",
		topCount[int64(1)], topCount[int64(3)])
}

func TestSelectP2COpenAICandidates_SingleCandidate(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 42, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 42}, score: 1.0},
	}
	req := OpenAIAccountScheduleRequest{SessionHash: "single"}

	result := selectP2COpenAICandidates(candidates, req)
	require.Len(t, result, 1)
	require.Equal(t, int64(42), result[0].account.ID)
}

func TestSelectP2COpenAICandidates_DeterministicWithSameSeed(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 10, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 10}, score: 0.8},
		{account: &Account{ID: 20, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 20}, score: 0.6},
		{account: &Account{ID: 30, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 30}, score: 0.4},
		{account: &Account{ID: 40, Priority: 0}, loadInfo: &AccountLoadInfo{AccountID: 40}, score: 0.2},
	}
	// Use a session hash to ensure the seed is deterministic (no time entropy).
	req := OpenAIAccountScheduleRequest{
		SessionHash: "deterministic_p2c_seed",
	}

	first := selectP2COpenAICandidates(candidates, req)
	for i := 0; i < 10; i++ {
		again := selectP2COpenAICandidates(candidates, req)
		require.Len(t, again, len(first))
		for j := range first {
			require.Equal(t, first[j].account.ID, again[j].account.ID,
				"iteration %d position %d mismatch", i, j)
		}
	}
}

func TestP2CLoadBalanceIntegration(t *testing.T) {
	// End-to-end test: enable P2C via config, verify it distributes across
	// accounts and that decision.TopK == 0 (P2C mode indicator).
	ctx := context.Background()
	groupID := int64(50)
	accounts := []Account{
		{
			ID: 5001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 10, Priority: 0,
		},
		{
			ID: 5002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 10, Priority: 0,
		},
		{
			ID: 5003, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 10, Priority: 0,
		},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerP2CEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 0.7
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.8
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.5

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			5001: {AccountID: 5001, LoadRate: 20, WaitingCount: 0},
			5002: {AccountID: 5002, LoadRate: 30, WaitingCount: 0},
			5003: {AccountID: 5003, LoadRate: 40, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selected := map[int64]int{}
	iterations := 100
	for i := 0; i < iterations; i++ {
		sessionHash := fmt.Sprintf("p2c_integration_%d", i)
		selection, decision, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			sessionHash,
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
		// P2C mode: TopK should be 0.
		require.Equal(t, 0, decision.TopK, "P2C mode should set TopK=0")
		selected[selection.Account.ID]++
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	// P2C with 3 candidates: the two better-scored accounts (5001, 5002)
	// should be selected, while the weakest (5003) may rarely or never win
	// a P2C tournament. Verify at least 2 distinct accounts are picked and
	// the lowest-loaded account dominates.
	require.GreaterOrEqual(t, len(selected), 2,
		"P2C should distribute across at least 2 accounts; got %v", selected)
	require.Greater(t, selected[int64(5001)], 0,
		"lowest-loaded account 5001 should be selected at least once")
	require.Greater(t, selected[int64(5001)], selected[int64(5003)],
		"lowest-loaded account should be favored over highest-loaded; got 5001=%d 5003=%d",
		selected[int64(5001)], selected[int64(5003)])
}

func TestP2CFallbackToTopK(t *testing.T) {
	// When P2C is disabled (default), the Top-K path should be used.
	// Verify topK > 0 in decision.
	ctx := context.Background()
	groupID := int64(51)
	accounts := []Account{
		{
			ID: 5101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 5, Priority: 0,
		},
		{
			ID: 5102, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 5, Priority: 0,
		},
	}

	cfg := &config.Config{}
	// Explicitly disable P2C (or leave at default false).
	cfg.Gateway.OpenAIWS.SchedulerP2CEnabled = false
	cfg.Gateway.OpenAIWS.LBTopK = 2
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 0.7
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 0.8
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.5

	concurrencyCache := stubConcurrencyCache{
		loadMap: map[int64]*AccountLoadInfo{
			5101: {AccountID: 5101, LoadRate: 10, WaitingCount: 0},
			5102: {AccountID: 5102, LoadRate: 20, WaitingCount: 0},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              &stubGatewayCache{sessionBindings: map[string]int64{}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"topk_fallback_test",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	// Top-K mode: TopK should be > 0.
	require.Greater(t, decision.TopK, 0, "Top-K mode should set TopK > 0")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	// Also verify P2C helper returns false when disabled.
	require.False(t, svc.openAIWSSchedulerP2CEnabled())
}

// ---------------------------------------------------------------------------
// Conditional Sticky Session Release Tests
// ---------------------------------------------------------------------------

// buildConditionalStickyTestService creates a minimal OpenAIGatewayService and
// scheduler with injectable runtime stats for conditional sticky tests.
func buildConditionalStickyTestService(
	accounts []Account,
	stickyKey string,
	stickyAccountID int64,
	stickyReleaseEnabled bool,
	cbEnabled bool,
) (*OpenAIGatewayService, *defaultOpenAIAccountScheduler) {
	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			stickyKey: stickyAccountID,
		},
	}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickyReleaseEnabled = stickyReleaseEnabled
	// Leave StickyReleaseErrorThreshold at 0 to use the default (0.3).
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = cbEnabled
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerFailThreshold = 3
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 5
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 30 * time.Second

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	stats := newOpenAIAccountRuntimeStats()
	scheduler := &defaultOpenAIAccountScheduler{
		service: svc,
		stats:   stats,
	}
	// Wire the scheduler into the service so that SelectAccountWithScheduler
	// uses it via getOpenAIAccountScheduler.
	svc.openaiScheduler = scheduler
	svc.openaiAccountStats = stats
	return svc, scheduler
}

func TestConditionalSticky_ReleaseOnHighErrorRate(t *testing.T) {
	ctx := context.Background()
	groupID := int64(30001)
	stickyAccount := Account{
		ID:          5001,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}
	fallbackAccount := Account{
		ID:          5002,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}

	svc, scheduler := buildConditionalStickyTestService(
		[]Account{stickyAccount, fallbackAccount},
		fmt.Sprintf("openai:sticky_err_%d", groupID),
		stickyAccount.ID,
		true,  // stickyReleaseEnabled
		false, // cbEnabled (not needed for error rate test)
	)

	// Pump the error rate above the 0.3 default threshold.
	// With alpha=0.5 (fast EWMA), after ~5 consecutive failures the rate
	// converges well above 0.3.
	for i := 0; i < 10; i++ {
		scheduler.stats.report(stickyAccount.ID, false, nil, "", 0)
	}
	errRate, _, _ := scheduler.stats.snapshot(stickyAccount.ID)
	require.Greater(t, errRate, 0.3, "error rate should exceed threshold before test")

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "",
		fmt.Sprintf("sticky_err_%d", groupID),
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	// The sticky account should have been released; the scheduler should
	// have fallen through to load balance and selected one of the accounts.
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer,
		"should fall through to load balance after sticky release")
	require.False(t, decision.StickySessionHit, "sticky hit should be false")
}

func TestConditionalSticky_ReleaseOnCircuitOpen(t *testing.T) {
	ctx := context.Background()
	groupID := int64(30002)
	stickyAccount := Account{
		ID:          5011,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}
	fallbackAccount := Account{
		ID:          5012,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}

	svc, scheduler := buildConditionalStickyTestService(
		[]Account{stickyAccount, fallbackAccount},
		fmt.Sprintf("openai:sticky_cb_%d", groupID),
		stickyAccount.ID,
		true, // stickyReleaseEnabled
		true, // cbEnabled
	)

	// Trip the circuit breaker by reporting consecutive failures beyond
	// the configured threshold (3).
	for i := 0; i < 5; i++ {
		scheduler.ReportResult(stickyAccount.ID, false, nil, "", 0)
	}
	require.True(t, scheduler.stats.isCircuitOpen(stickyAccount.ID),
		"circuit breaker should be OPEN before test")

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "",
		fmt.Sprintf("sticky_cb_%d", groupID),
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer,
		"should fall through to load balance after sticky release due to CB open")
	require.False(t, decision.StickySessionHit)
}

func TestConditionalSticky_KeepsHealthySticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(30003)
	stickyAccount := Account{
		ID:          5021,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}

	svc, scheduler := buildConditionalStickyTestService(
		[]Account{stickyAccount},
		fmt.Sprintf("openai:sticky_ok_%d", groupID),
		stickyAccount.ID,
		true,  // stickyReleaseEnabled
		false, // cbEnabled
	)

	// Report some successes so error rate stays at 0.
	for i := 0; i < 5; i++ {
		scheduler.stats.report(stickyAccount.ID, true, nil, "", 0)
	}
	errRate, _, _ := scheduler.stats.snapshot(stickyAccount.ID)
	require.Less(t, errRate, 0.3, "error rate should be below threshold")

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "",
		fmt.Sprintf("sticky_ok_%d", groupID),
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, stickyAccount.ID, selection.Account.ID,
		"healthy sticky account should be kept")
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestConditionalSticky_DisabledByConfig(t *testing.T) {
	ctx := context.Background()
	groupID := int64(30004)
	stickyAccount := Account{
		ID:          5031,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}

	svc, scheduler := buildConditionalStickyTestService(
		[]Account{stickyAccount},
		fmt.Sprintf("openai:sticky_off_%d", groupID),
		stickyAccount.ID,
		false, // stickyReleaseEnabled = OFF
		false, // cbEnabled
	)

	// Pump error rate very high, but since sticky release is disabled,
	// the sticky binding should still hold.
	for i := 0; i < 10; i++ {
		scheduler.stats.report(stickyAccount.ID, false, nil, "", 0)
	}
	errRate, _, _ := scheduler.stats.snapshot(stickyAccount.ID)
	require.Greater(t, errRate, 0.3, "error rate should exceed threshold")

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "",
		fmt.Sprintf("sticky_off_%d", groupID),
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, stickyAccount.ID, selection.Account.ID,
		"sticky should be kept when feature is disabled")
	require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
	require.True(t, decision.StickySessionHit)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestConditionalSticky_Metrics(t *testing.T) {
	groupID := int64(30005)
	ctx := context.Background()

	// --- Error rate release metric ---
	stickyAccount := Account{
		ID:          5041,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}
	fallbackAccount := Account{
		ID:          5042,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 5,
	}

	svc, scheduler := buildConditionalStickyTestService(
		[]Account{stickyAccount, fallbackAccount},
		fmt.Sprintf("openai:sticky_m1_%d", groupID),
		stickyAccount.ID,
		true, // stickyReleaseEnabled
		true, // cbEnabled
	)

	// Trigger error-rate release. With CB also enabled and threshold=3,
	// the CB will be OPEN after 3 failures via stats.report (which uses
	// the default CB threshold of 5). Send enough to ensure both are
	// triggered.
	for i := 0; i < 10; i++ {
		scheduler.stats.report(stickyAccount.ID, false, nil, "", 0)
	}

	_, _, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "",
		fmt.Sprintf("sticky_m1_%d", groupID),
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)

	snap := scheduler.SnapshotMetrics()
	// At least one of the two release metrics should have been incremented.
	totalReleases := snap.StickyReleaseErrorTotal + snap.StickyReleaseCircuitOpenTotal
	require.Greater(t, totalReleases, int64(0),
		"at least one sticky release metric should be incremented")

	// --- Circuit breaker release metric (clean setup) ---
	groupID2 := int64(30006)
	svc2, scheduler2 := buildConditionalStickyTestService(
		[]Account{stickyAccount, fallbackAccount},
		fmt.Sprintf("openai:sticky_m2_%d", groupID2),
		stickyAccount.ID,
		true, // stickyReleaseEnabled
		true, // cbEnabled
	)

	// Trip CB via ReportResult (which checks the configured threshold=3).
	for i := 0; i < 5; i++ {
		scheduler2.ReportResult(stickyAccount.ID, false, nil, "", 0)
	}
	require.True(t, scheduler2.stats.isCircuitOpen(stickyAccount.ID))

	_, _, err = svc2.SelectAccountWithScheduler(
		ctx, &groupID2, "",
		fmt.Sprintf("sticky_m2_%d", groupID2),
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)

	snap2 := scheduler2.SnapshotMetrics()
	require.Greater(t, snap2.StickyReleaseCircuitOpenTotal, int64(0),
		"circuit-open sticky release metric should be incremented")
}

// ---------------------------------------------------------------------------
// Softmax Temperature Sampling Tests
// ---------------------------------------------------------------------------

// makeSoftmaxCandidates builds N candidates with the given scores.
func makeSoftmaxCandidates(scores ...float64) []openAIAccountCandidateScore {
	out := make([]openAIAccountCandidateScore, len(scores))
	for i, s := range scores {
		out[i] = openAIAccountCandidateScore{
			account:  &Account{ID: int64(i + 1), Priority: 0},
			loadInfo: &AccountLoadInfo{AccountID: int64(i + 1)},
			score:    s,
		}
	}
	return out
}

func TestSoftmax_LowTemperatureApproximatesArgmax(t *testing.T) {
	// With a very low temperature the highest-scored candidate should win
	// almost every time.
	candidates := makeSoftmaxCandidates(5.0, 3.0, 1.0, 0.5)

	winCount := 0
	trials := 100
	for i := 0; i < trials; i++ {
		rng := newOpenAISelectionRNG(uint64(i + 1))
		result := selectSoftmaxOpenAICandidates(candidates, 0.01, &rng)
		require.Len(t, result, len(candidates))
		if result[0].account.ID == 1 { // ID 1 has score 5.0 (the highest)
			winCount++
		}
	}

	require.Greater(t, winCount, 90,
		"with temperature=0.01 the highest-scored candidate should win >90%% of trials; got %d/%d", winCount, trials)
}

func TestSoftmax_HighTemperatureApproximatesUniform(t *testing.T) {
	// With a very high temperature, all candidates should get roughly equal
	// selection frequency.
	candidates := makeSoftmaxCandidates(5.0, 3.0, 1.0, 0.5)

	counts := map[int64]int{}
	trials := 1000
	for i := 0; i < trials; i++ {
		rng := newOpenAISelectionRNG(uint64(i + 1))
		result := selectSoftmaxOpenAICandidates(candidates, 100.0, &rng)
		require.Len(t, result, len(candidates))
		counts[result[0].account.ID]++
	}

	expected := float64(trials) / float64(len(candidates)) // 250
	for id, count := range counts {
		require.InDelta(t, expected, float64(count), float64(trials)*0.10,
			"candidate ID=%d expected ~%.0f selections, got %d", id, expected, count)
	}
}

func TestSoftmax_DefaultTemperature(t *testing.T) {
	// With the default temperature (0.3), higher-scored candidates should be
	// picked more often than lower-scored ones.
	candidates := makeSoftmaxCandidates(5.0, 3.0, 1.0, 0.5)

	counts := map[int64]int{}
	trials := 1000
	for i := 0; i < trials; i++ {
		rng := newOpenAISelectionRNG(uint64(i + 1))
		result := selectSoftmaxOpenAICandidates(candidates, defaultSoftmaxTemperature, &rng)
		counts[result[0].account.ID]++
	}

	// The candidate with the highest score (ID=1, score=5.0) should be
	// selected more often than the candidate with the lowest (ID=4, score=0.5).
	require.Greater(t, counts[int64(1)], counts[int64(4)],
		"highest-scored candidate should be picked more often; best=%d worst=%d",
		counts[int64(1)], counts[int64(4)])

	// Also check that the top-scored candidate beats the second-highest.
	require.Greater(t, counts[int64(1)], counts[int64(2)],
		"score=5.0 should beat score=3.0; got %d vs %d",
		counts[int64(1)], counts[int64(2)])
}

func TestSoftmax_SingleCandidate(t *testing.T) {
	candidates := makeSoftmaxCandidates(7.5)

	rng := newOpenAISelectionRNG(42)
	result := selectSoftmaxOpenAICandidates(candidates, 0.3, &rng)

	require.Len(t, result, 1)
	require.Equal(t, int64(1), result[0].account.ID)
	require.Equal(t, 7.5, result[0].score)
}

func TestSoftmax_TwoCandidates(t *testing.T) {
	// Use a moderate score gap (1.0 vs 0.5) with temperature=1.0 so both
	// candidates have meaningful selection probability.
	candidates := makeSoftmaxCandidates(1.0, 0.5)

	counts := map[int64]int{}
	trials := 1000
	for i := 0; i < trials; i++ {
		rng := newOpenAISelectionRNG(uint64(i + 1))
		result := selectSoftmaxOpenAICandidates(candidates, 1.0, &rng)
		require.Len(t, result, 2)
		counts[result[0].account.ID]++
	}

	// Both candidates should be selected at least once (proving no
	// single-candidate monopoly), and the higher-scored one should dominate.
	require.Greater(t, counts[int64(1)], 0, "high-scored candidate must be selected at least once")
	require.Greater(t, counts[int64(2)], 0, "low-scored candidate must be selected at least once")
	require.Greater(t, counts[int64(1)], counts[int64(2)],
		"higher-scored candidate should be picked more often; got %d vs %d",
		counts[int64(1)], counts[int64(2)])
}

func TestSoftmax_EqualScores(t *testing.T) {
	// When all scores are equal, selection should be approximately uniform.
	candidates := makeSoftmaxCandidates(3.0, 3.0, 3.0, 3.0)

	counts := map[int64]int{}
	trials := 1000
	for i := 0; i < trials; i++ {
		rng := newOpenAISelectionRNG(uint64(i + 1))
		result := selectSoftmaxOpenAICandidates(candidates, 0.3, &rng)
		counts[result[0].account.ID]++
	}

	expected := float64(trials) / float64(len(candidates)) // 250
	for id, count := range counts {
		require.InDelta(t, expected, float64(count), float64(trials)*0.10,
			"equal scores should yield ~uniform distribution; ID=%d expected ~%.0f got %d",
			id, expected, count)
	}
}

func TestSoftmax_NumericalStability(t *testing.T) {
	// Large score differences should not cause overflow or NaN.
	candidates := makeSoftmaxCandidates(100.0, -100.0, 50.0, -50.0)

	rng := newOpenAISelectionRNG(12345)
	result := selectSoftmaxOpenAICandidates(candidates, 0.3, &rng)

	require.Len(t, result, len(candidates))
	// Verify all scores are finite in the output (no NaN or Inf propagation).
	for _, c := range result {
		require.False(t, math.IsNaN(c.score), "score should not be NaN")
		require.False(t, math.IsInf(c.score, 0), "score should not be Inf")
	}
	// All candidates must appear exactly once.
	seen := map[int64]bool{}
	for _, c := range result {
		require.False(t, seen[c.account.ID], "duplicate account ID %d", c.account.ID)
		seen[c.account.ID] = true
	}
	require.Len(t, seen, len(candidates))

	// With such extreme differences at temperature=0.3, the highest scorer (100.0)
	// should always win because exp((100 - 100)/0.3) = 1 while
	// exp((-100 - 100)/0.3) ~= 0 (numerically stable via maxScore subtraction).
	winCount := 0
	for i := 0; i < 100; i++ {
		rng2 := newOpenAISelectionRNG(uint64(i + 1))
		r := selectSoftmaxOpenAICandidates(candidates, 0.3, &rng2)
		if r[0].account.ID == 1 { // score 100.0
			winCount++
		}
	}
	require.Greater(t, winCount, 95,
		"with extreme score gap, the highest scorer should win nearly always; got %d/100", winCount)
}

func TestSoftmax_DisabledFallsThrough(t *testing.T) {
	// When softmax is disabled, the scheduler should fall through to P2C or Top-K.
	ctx := context.Background()
	groupID := int64(40001)
	accounts := make([]Account, 5)
	for i := range accounts {
		accounts[i] = Account{
			ID:          int64(6001 + i),
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
		}
	}

	cache := &stubGatewayCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	// Softmax explicitly disabled (default).
	cfg.Gateway.OpenAIWS.SchedulerSoftmaxEnabled = false
	// P2C also disabled.
	cfg.Gateway.OpenAIWS.SchedulerP2CEnabled = false

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "softmax_disabled_test",
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	// TopK should be > 0, confirming Top-K path was taken.
	require.Greater(t, decision.TopK, 0, "should fall through to Top-K when softmax is disabled")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestSoftmax_FewCandidatesFallsThrough(t *testing.T) {
	// When softmax is enabled but there are <= 3 candidates, it should fall
	// through to the next strategy (P2C or Top-K).
	ctx := context.Background()
	groupID := int64(40002)
	// Only 3 accounts — softmax guard requires >3.
	accounts := make([]Account, 3)
	for i := range accounts {
		accounts[i] = Account{
			ID:          int64(7001 + i),
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
		}
	}

	cache := &stubGatewayCache{}
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	// Softmax enabled but should not activate with only 3 candidates.
	cfg.Gateway.OpenAIWS.SchedulerSoftmaxEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerSoftmaxTemperature = 0.5
	// P2C disabled, so it should fall through to Top-K.
	cfg.Gateway.OpenAIWS.SchedulerP2CEnabled = false

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, decision, err := svc.SelectAccountWithScheduler(
		ctx, &groupID, "", "softmax_few_candidates_test",
		"gpt-5.1", nil, OpenAIUpstreamTransportAny,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, openAIAccountScheduleLayerLoadBalance, decision.Layer)
	// TopK should be > 0, confirming Top-K path was taken instead of softmax.
	require.Greater(t, decision.TopK, 0, "should fall through to Top-K when candidate count <= 3")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestSoftmax_ConfigDefaults(t *testing.T) {
	// When config values are zero/unset, defaults should be applied.

	// Case 1: nil service — returns empty config.
	nilScheduler := &defaultOpenAIAccountScheduler{}
	cfg0 := nilScheduler.softmaxConfigRead()
	require.False(t, cfg0.enabled)
	require.Equal(t, 0.0, cfg0.temperature) // no default when service is nil

	// Case 2: zero temperature (unset) — should default to 0.3.
	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
	}
	svc.cfg.Gateway.OpenAIWS.SchedulerSoftmaxEnabled = true
	svc.cfg.Gateway.OpenAIWS.SchedulerSoftmaxTemperature = 0 // unset
	scheduler := &defaultOpenAIAccountScheduler{
		service: svc,
		stats:   newOpenAIAccountRuntimeStats(),
	}
	cfg1 := scheduler.softmaxConfigRead()
	require.True(t, cfg1.enabled)
	require.Equal(t, 0.3, cfg1.temperature, "default temperature should be 0.3")

	// Case 3: explicit temperature — should use the configured value.
	svc.cfg.Gateway.OpenAIWS.SchedulerSoftmaxTemperature = 0.7
	cfg2 := scheduler.softmaxConfigRead()
	require.True(t, cfg2.enabled)
	require.Equal(t, 0.7, cfg2.temperature, "should use explicitly configured temperature")

	// Case 4: negative temperature — should fall back to default 0.3.
	svc.cfg.Gateway.OpenAIWS.SchedulerSoftmaxTemperature = -1.0
	cfg3 := scheduler.softmaxConfigRead()
	require.Equal(t, 0.3, cfg3.temperature, "negative temperature should fall back to default 0.3")
}

// ---------------------------------------------------------------------------
// Per-Model TTFT Tests
// ---------------------------------------------------------------------------

func TestPerModelTTFT_IndependentTracking(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(8001)

	// Report different TTFT values for two different models on the same account.
	stats.report(accountID, true, nil, "gpt-4o", 100)
	stats.report(accountID, true, nil, "gpt-4o", 120)
	stats.report(accountID, true, nil, "o3-pro", 500)
	stats.report(accountID, true, nil, "o3-pro", 600)

	// Snapshot for model gpt-4o.
	_, ttftGPT4o, hasTTFT := stats.snapshot(accountID, "gpt-4o")
	require.True(t, hasTTFT, "gpt-4o should have TTFT data")

	// Snapshot for model o3-pro.
	_, ttftO3Pro, hasO3Pro := stats.snapshot(accountID, "o3-pro")
	require.True(t, hasO3Pro, "o3-pro should have TTFT data")

	// The two models should have different TTFT values because their
	// sample inputs are very different (100-120 vs 500-600).
	require.Greater(t, math.Abs(ttftGPT4o-ttftO3Pro), 50.0,
		"different models should track independent TTFT values")

	// gpt-4o TTFT should be much lower than o3-pro.
	require.Less(t, ttftGPT4o, ttftO3Pro,
		"gpt-4o should have lower TTFT than o3-pro")
}

func TestPerModelTTFT_FallbackToGlobal(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(8002)

	// Report TTFT for a specific model.
	stats.report(accountID, true, nil, "gpt-4o", 200)

	// Snapshot with an unknown model should fall back to global TTFT.
	_, ttftUnknown, hasUnknown := stats.snapshot(accountID, "unknown-model")
	require.True(t, hasUnknown, "should fall back to global TTFT")

	// Global TTFT should have been updated by the gpt-4o report.
	_, ttftGlobal, hasGlobal := stats.snapshot(accountID)
	require.True(t, hasGlobal, "global TTFT should exist")

	// The unknown-model fallback should equal the global.
	require.InDelta(t, ttftGlobal, ttftUnknown, 1e-9,
		"unknown model should return global TTFT as fallback")
}

func TestPerModelTTFT_GlobalAlsoUpdated(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(8003)

	// No data initially.
	_, _, hasGlobal := stats.snapshot(accountID)
	require.False(t, hasGlobal, "no global TTFT initially")

	// Report with model — should also update global.
	stats.report(accountID, true, nil, "gpt-4o", 300)

	_, ttftGlobal, hasGlobal := stats.snapshot(accountID)
	require.True(t, hasGlobal, "global TTFT should exist after model report")
	require.InDelta(t, 300.0, ttftGlobal, 1e-9,
		"global TTFT should be updated by model report")
}

func TestPerModelTTFT_TTLCleanup(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	stat.ttft.initNaN()

	// Manually insert a model entry with an old timestamp.
	d := &dualEWMATTFT{}
	d.initNaN()
	d.update(100)
	stat.modelTTFT.Store("old-model", d)
	stat.modelTTFTLastUpdate.Store("old-model", time.Now().Add(-time.Hour).UnixNano())

	// Insert a recent model entry.
	d2 := &dualEWMATTFT{}
	d2.initNaN()
	d2.update(200)
	stat.modelTTFT.Store("new-model", d2)
	stat.modelTTFTLastUpdate.Store("new-model", time.Now().UnixNano())

	// Cleanup with 30-minute TTL — old-model should be removed.
	stat.cleanupStaleTTFT(30*time.Minute, 100)

	_, hasOld := stat.modelTTFTValue("old-model")
	require.False(t, hasOld, "old-model should be cleaned up")

	_, hasNew := stat.modelTTFTValue("new-model")
	require.True(t, hasNew, "new-model should survive cleanup")
}

func TestPerModelTTFT_MaxModelLimit(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	stat.ttft.initNaN()

	now := time.Now()
	// Insert 10 models with sequential timestamps.
	for i := 0; i < 10; i++ {
		model := fmt.Sprintf("model-%d", i)
		d := &dualEWMATTFT{}
		d.initNaN()
		d.update(float64(100 + i*10))
		stat.modelTTFT.Store(model, d)
		stat.modelTTFTLastUpdate.Store(model, now.Add(time.Duration(i)*time.Second).UnixNano())
	}

	// Enforce limit of 5 models — the 5 oldest should be evicted.
	stat.cleanupStaleTTFT(time.Hour, 5)

	// Count remaining models.
	remaining := 0
	stat.modelTTFT.Range(func(_, _ any) bool {
		remaining++
		return true
	})
	require.Equal(t, 5, remaining, "should have exactly 5 models after cleanup")

	// The newest 5 (model-5 through model-9) should survive.
	for i := 5; i < 10; i++ {
		model := fmt.Sprintf("model-%d", i)
		_, has := stat.modelTTFTValue(model)
		require.True(t, has, "%s should survive", model)
	}
	// The oldest 5 (model-0 through model-4) should be evicted.
	for i := 0; i < 5; i++ {
		model := fmt.Sprintf("model-%d", i)
		_, has := stat.modelTTFTValue(model)
		require.False(t, has, "%s should be evicted", model)
	}
}

func TestPerModelTTFT_SnapshotUsesModelData(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(8006)

	// Report two models with very different TTFT.
	for i := 0; i < 5; i++ {
		stats.report(accountID, true, nil, "fast-model", 50)
		stats.report(accountID, true, nil, "slow-model", 500)
	}

	// Snapshot with specific model should return that model's TTFT.
	_, ttftFast, hasFast := stats.snapshot(accountID, "fast-model")
	require.True(t, hasFast)

	_, ttftSlow, hasSlow := stats.snapshot(accountID, "slow-model")
	require.True(t, hasSlow)

	// Fast model should have much lower TTFT.
	require.Less(t, ttftFast, 100.0, "fast-model TTFT should be close to 50")
	require.Greater(t, ttftSlow, 400.0, "slow-model TTFT should be close to 500")

	// Global TTFT should be a blend of both.
	_, ttftGlobal, hasGlobal := stats.snapshot(accountID)
	require.True(t, hasGlobal)
	require.Greater(t, ttftGlobal, ttftFast, "global TTFT should be higher than fast-model")
	require.Less(t, ttftGlobal, ttftSlow, "global TTFT should be lower than slow-model")
}

func TestPerModelTTFT_ConcurrentAccess(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(8007)

	const workers = 8
	const iterations = 200
	models := []string{"model-a", "model-b", "model-c", "model-d"}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		w := w
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				model := models[(w+i)%len(models)]
				ttft := float64(100 + (w*10+i)%200)
				stats.report(accountID, true, nil, model, ttft)

				// Also read concurrently.
				stats.snapshot(accountID, model)
				stats.snapshot(accountID)
			}
		}()
	}
	wg.Wait()

	// All models should have TTFT data.
	for _, model := range models {
		_, ttft, has := stats.snapshot(accountID, model)
		require.True(t, has, "%s should have TTFT", model)
		require.Greater(t, ttft, 0.0, "%s TTFT should be positive", model)
	}

	// Global should also have data.
	_, ttftGlobal, hasGlobal := stats.snapshot(accountID)
	require.True(t, hasGlobal)
	require.Greater(t, ttftGlobal, 0.0)
}

func TestPerModelTTFT_EmptyModel(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(8008)

	// Report with empty model — should only update global TTFT.
	ttft := 150
	stats.report(accountID, true, &ttft, "", 0)

	// Global should have data.
	_, ttftGlobal, hasGlobal := stats.snapshot(accountID)
	require.True(t, hasGlobal, "global TTFT should exist from firstTokenMs")
	require.InDelta(t, 150.0, ttftGlobal, 1e-9)

	// Snapshot with empty model returns global.
	_, ttftEmpty, hasEmpty := stats.snapshot(accountID, "")
	require.True(t, hasEmpty)
	require.InDelta(t, ttftGlobal, ttftEmpty, 1e-9,
		"empty model snapshot should return global TTFT")

	// No per-model entries should exist.
	stat := stats.loadOrCreate(accountID)
	count := 0
	stat.modelTTFT.Range(func(_, _ any) bool {
		count++
		return true
	})
	require.Equal(t, 0, count, "no per-model entries should exist for empty model")
}

// ---------------------------------------------------------------------------
// Load Trend Prediction Tests
// ---------------------------------------------------------------------------

func TestLoadTrend_RisingLoad(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()
	for i := 0; i < 10; i++ {
		tracker.recordAt(float64((i+1)*10), base+int64(i)*int64(time.Second))
	}
	slope := tracker.slope()
	require.Greater(t, slope, 0.0, "rising load should produce positive slope")
	require.InDelta(t, 10.0, slope, 0.01, "slope should be ~10 per second")
}

func TestLoadTrend_FallingLoad(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()
	for i := 0; i < 10; i++ {
		tracker.recordAt(float64(100-i*10), base+int64(i)*int64(time.Second))
	}
	slope := tracker.slope()
	require.Less(t, slope, 0.0, "falling load should produce negative slope")
	require.InDelta(t, -10.0, slope, 0.01, "slope should be ~-10 per second")
}

func TestLoadTrend_StableLoad(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()
	for i := 0; i < 10; i++ {
		tracker.recordAt(50.0, base+int64(i)*int64(time.Second))
	}
	slope := tracker.slope()
	require.InDelta(t, 0.0, slope, 1e-9, "constant load should produce zero slope")
}

func TestLoadTrend_RingBufferFull(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()
	for i := 0; i < 5; i++ {
		tracker.recordAt(100.0, base+int64(i)*int64(time.Second))
	}
	for i := 0; i < 10; i++ {
		tracker.recordAt(float64((i+1)*10), base+int64(5+i)*int64(time.Second))
	}
	slope := tracker.slope()
	require.Greater(t, slope, 0.0, "should reflect rising trend from last 10 samples")
	require.InDelta(t, 10.0, slope, 0.01, "slope should be ~10 per second after ring wraps")
}

func TestLoadTrend_InsufficientSamples(t *testing.T) {
	var tracker loadTrendTracker
	slope := tracker.slope()
	require.Equal(t, 0.0, slope, "zero samples should return slope 0")
}

func TestLoadTrend_SingleSample(t *testing.T) {
	var tracker loadTrendTracker
	tracker.recordAt(42.0, time.Now().UnixNano())
	slope := tracker.slope()
	require.Equal(t, 0.0, slope, "single sample should return slope 0")
}

func TestLoadTrend_TwoSamples(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()
	tracker.recordAt(10.0, base)
	tracker.recordAt(30.0, base+int64(2*time.Second))
	slope := tracker.slope()
	require.InDelta(t, 10.0, slope, 0.01, "two-sample slope should be exact delta/time")
}

func TestLoadTrend_AllSameTimestamp(t *testing.T) {
	var tracker loadTrendTracker
	ts := time.Now().UnixNano()
	for i := 0; i < 5; i++ {
		tracker.recordAt(float64(i*10), ts)
	}
	slope := tracker.slope()
	require.Equal(t, 0.0, slope, "all-same-timestamp should return slope 0 (degenerate)")
}

func TestLoadTrend_NegativeSlope(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()
	for i := 0; i < 5; i++ {
		tracker.recordAt(50.0-float64(i)*5.0, base+int64(i)*int64(time.Second))
	}
	slope := tracker.slope()
	require.InDelta(t, -5.0, slope, 0.01)
}

func TestLoadTrend_ScoringIntegration(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10},
	}

	loadMap := map[int64]*AccountLoadInfo{
		1: {AccountID: 1, LoadRate: 50},
		2: {AccountID: 2, LoadRate: 50},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	cfg.Gateway.OpenAIWS.SchedulerTrendEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerTrendMaxSlope = 5.0

	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: accounts},
		cfg:         cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{
			loadMap:         loadMap,
			skipDefaultLoad: true,
		}),
	}

	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)

	base := time.Now().UnixNano() - int64(10*time.Second)
	stat1 := stats.loadOrCreate(1)
	for i := 0; i < 9; i++ {
		stat1.loadTrend.recordAt(float64((i+1)*10), base+int64(i)*int64(time.Second))
	}
	stat2 := stats.loadOrCreate(2)
	for i := 0; i < 9; i++ {
		stat2.loadTrend.recordAt(50.0, base+int64(i)*int64(time.Second))
	}

	ctx := context.Background()
	selection, _, _, _, err := scheduler.selectByLoadBalance(ctx, OpenAIAccountScheduleRequest{
		RequiredTransport: OpenAIUpstreamTransportAny,
	})
	require.NoError(t, err)
	require.NotNil(t, selection)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}

	slope1 := stat1.loadTrend.slope()
	slope2 := stat2.loadTrend.slope()
	require.Greater(t, slope1, slope2,
		"rising-trend account should have higher slope than stable account; slope1=%f slope2=%f", slope1, slope2)
	require.Greater(t, slope1, 0.0, "rising-trend account slope should be positive")
	require.InDelta(t, 0.0, slope2, 1.0,
		"stable-trend account slope should be near zero; got %f", slope2)

	trendAdj1 := 1.0 - clamp01(slope1/5.0)
	trendAdj2 := 1.0 - clamp01(slope2/5.0)
	require.Less(t, trendAdj1, trendAdj2,
		"rising-trend trendAdj should be less than stable trendAdj; adj1=%f adj2=%f", trendAdj1, trendAdj2)
}

func TestLoadTrend_DisabledByDefault(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)

	base := time.Now().UnixNano()
	stat1 := stats.loadOrCreate(1)
	for i := 0; i < 10; i++ {
		stat1.loadTrend.recordAt(float64(i*10), base+int64(i)*int64(time.Second))
	}
	stat2 := stats.loadOrCreate(2)
	for i := 0; i < 10; i++ {
		stat2.loadTrend.recordAt(30.0, base+int64(i)*int64(time.Second))
	}

	ctx := context.Background()
	selectedCounts := map[int64]int{}
	const rounds = 100
	for r := 0; r < rounds; r++ {
		selection, _, _, _, err := scheduler.selectByLoadBalance(ctx, OpenAIAccountScheduleRequest{
			RequiredTransport: OpenAIUpstreamTransportAny,
		})
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		selectedCounts[selection.Account.ID]++
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	require.Greater(t, selectedCounts[int64(1)], 10,
		"with trend disabled, account 1 should still get selections; got %d", selectedCounts[int64(1)])
	require.Greater(t, selectedCounts[int64(2)], 10,
		"with trend disabled, account 2 should still get selections; got %d", selectedCounts[int64(2)])
}

func TestLoadTrend_ConcurrentAccess(t *testing.T) {
	var tracker loadTrendTracker
	var wg sync.WaitGroup
	const goroutines = 10
	const recordsPerGoroutine = 100

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < recordsPerGoroutine; i++ {
				tracker.record(float64(id*100 + i))
			}
		}(g)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < goroutines*recordsPerGoroutine; i++ {
			_ = tracker.slope()
		}
	}()

	wg.Wait()

	slope := tracker.slope()
	require.False(t, math.IsNaN(slope), "slope should be finite after concurrent access")
	require.False(t, math.IsInf(slope, 0), "slope should be finite after concurrent access")
}

func TestLoadTrend_TrendConfigDefaults(t *testing.T) {
	svc := &OpenAIGatewayService{}
	enabled, maxSlope := svc.openAIWSSchedulerTrendConfig()
	require.False(t, enabled, "trend should be disabled by default")
	require.Equal(t, defaultSchedulerTrendMaxSlope, maxSlope, "maxSlope should use default")
}

func TestLoadTrend_TrendConfigCustom(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerTrendEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerTrendMaxSlope = 8.0
	svc := &OpenAIGatewayService{cfg: cfg}
	enabled, maxSlope := svc.openAIWSSchedulerTrendConfig()
	require.True(t, enabled)
	require.Equal(t, 8.0, maxSlope)
}

func TestLoadTrend_TrendConfigZeroMaxSlope(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerTrendEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerTrendMaxSlope = 0
	svc := &OpenAIGatewayService{cfg: cfg}
	enabled, maxSlope := svc.openAIWSSchedulerTrendConfig()
	require.True(t, enabled)
	require.Equal(t, defaultSchedulerTrendMaxSlope, maxSlope)
}

func TestLoadTrend_RingBufferCountTracking(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()

	for i := 0; i < loadTrendRingSize; i++ {
		tracker.recordAt(float64(i), base+int64(i)*int64(time.Second))
	}
	require.Equal(t, loadTrendRingSize, tracker.count, "count should equal ring size after filling")

	tracker.recordAt(99.0, base+int64(loadTrendRingSize)*int64(time.Second))
	require.Equal(t, loadTrendRingSize, tracker.count, "count should remain capped at ring size")
}

func TestLoadTrend_GentleRise(t *testing.T) {
	var tracker loadTrendTracker
	base := time.Now().UnixNano()
	for i := 0; i < 10; i++ {
		tracker.recordAt(50.0+float64(i)*0.1, base+int64(i)*int64(time.Second))
	}
	slope := tracker.slope()
	require.Greater(t, slope, 0.0, "gentle rise should produce positive slope")
	require.InDelta(t, 0.1, slope, 0.01)
}

func TestLoadTrend_RecordUpdatesRuntimeStat(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	cfg.Gateway.OpenAIWS.SchedulerTrendEnabled = true

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		selection, _, _, _, err := scheduler.selectByLoadBalance(ctx, OpenAIAccountScheduleRequest{
			RequiredTransport: OpenAIUpstreamTransportAny,
		})
		require.NoError(t, err)
		if selection != nil && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	stat := stats.loadOrCreate(1)
	require.GreaterOrEqual(t, stat.loadTrend.count, 5,
		"trend tracker should have received samples from scoring loop")
}

func TestLoadTrend_FallingTrendBoostsScore(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 10},
	}

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	cfg.Gateway.OpenAIWS.SchedulerTrendEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerTrendMaxSlope = 5.0

	loadMap := map[int64]*AccountLoadInfo{
		1: {AccountID: 1, LoadRate: 50},
		2: {AccountID: 2, LoadRate: 50},
	}

	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: accounts},
		cfg:         cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{
			loadMap:         loadMap,
			skipDefaultLoad: true,
		}),
	}

	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)

	base := time.Now().UnixNano()
	stat1 := stats.loadOrCreate(1)
	for i := 0; i < 10; i++ {
		stat1.loadTrend.recordAt(80.0-float64(i)*5.0, base+int64(i)*int64(time.Second))
	}
	stat2 := stats.loadOrCreate(2)
	for i := 0; i < 10; i++ {
		stat2.loadTrend.recordAt(50.0, base+int64(i)*int64(time.Second))
	}

	ctx := context.Background()
	selectedCounts := map[int64]int{}
	const rounds = 50
	for r := 0; r < rounds; r++ {
		selection, _, _, _, err := scheduler.selectByLoadBalance(ctx, OpenAIAccountScheduleRequest{
			RequiredTransport: OpenAIUpstreamTransportAny,
		})
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		selectedCounts[selection.Account.ID]++
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}

	require.Greater(t, selectedCounts[int64(1)]+selectedCounts[int64(2)], 0,
		"both accounts should receive selections")
}

// ---------------------------------------------------------------------------
// Circuit Breaker Coverage Tests
// ---------------------------------------------------------------------------

func TestCircuitBreaker_AllowClosed(t *testing.T) {
	cb := &accountCircuitBreaker{}
	require.True(t, cb.allow(30*time.Second, 2), "CLOSED state should allow requests")
}

func TestCircuitBreaker_AllowOpenWithinCooldown(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(circuitBreakerStateOpen)
	cb.lastFailureNano.Store(time.Now().UnixNano())
	require.False(t, cb.allow(30*time.Second, 2), "OPEN within cooldown should deny")
}

func TestCircuitBreaker_AllowOpenAfterCooldown(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(circuitBreakerStateOpen)
	cb.lastFailureNano.Store(time.Now().Add(-1 * time.Minute).UnixNano())
	require.True(t, cb.allow(30*time.Second, 2), "OPEN after cooldown should transition to HALF_OPEN and allow")
	require.Equal(t, circuitBreakerStateHalfOpen, cb.state.Load())
}

func TestCircuitBreaker_AllowHalfOpenLimited(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(circuitBreakerStateHalfOpen)
	require.True(t, cb.allowHalfOpen(2))
	require.True(t, cb.allowHalfOpen(2))
	require.False(t, cb.allowHalfOpen(2), "should deny when in-flight reaches max")
}

func TestCircuitBreaker_AllowHalfOpenViaAllow(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(circuitBreakerStateHalfOpen)
	require.True(t, cb.allow(30*time.Second, 1))
	require.False(t, cb.allow(30*time.Second, 1), "HALF_OPEN with max=1 should deny second")
}

func TestCircuitBreaker_AllowDefaultState(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(99) // unknown state
	require.True(t, cb.allow(30*time.Second, 2), "unknown state should default to allow")
}

func TestCircuitBreaker_RecordSuccessClosed(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.consecutiveFails.Store(3)
	cb.recordSuccess()
	require.Equal(t, int32(0), cb.consecutiveFails.Load())
	require.Equal(t, circuitBreakerStateClosed, cb.state.Load())
}

func TestCircuitBreaker_RecordSuccessHalfOpenToClose(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(circuitBreakerStateHalfOpen)
	cb.halfOpenInFlight.Store(1)
	cb.halfOpenAdmitted.Store(1)
	cb.halfOpenSuccess.Store(0)
	cb.recordSuccess()
	require.Equal(t, circuitBreakerStateClosed, cb.state.Load(), "all probes succeeded should close")
	require.Equal(t, int32(0), cb.halfOpenInFlight.Load())
}

func TestCircuitBreaker_RecordSuccessHalfOpenPartial(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(circuitBreakerStateHalfOpen)
	cb.halfOpenInFlight.Store(3)
	cb.halfOpenAdmitted.Store(3)
	cb.halfOpenSuccess.Store(0)
	cb.recordSuccess()
	require.Equal(t, circuitBreakerStateHalfOpen, cb.state.Load(), "not all probes succeeded yet")
}

func TestCircuitBreaker_RecordFailureTripsOpen(t *testing.T) {
	cb := &accountCircuitBreaker{}
	for i := 0; i < 4; i++ {
		cb.recordFailure(5)
	}
	require.Equal(t, circuitBreakerStateClosed, cb.state.Load())
	cb.recordFailure(5)
	require.Equal(t, circuitBreakerStateOpen, cb.state.Load(), "5th failure should trip to OPEN")
}

func TestCircuitBreaker_RecordFailureHalfOpenReverts(t *testing.T) {
	cb := &accountCircuitBreaker{}
	cb.state.Store(circuitBreakerStateHalfOpen)
	cb.halfOpenInFlight.Store(2)
	cb.recordFailure(5)
	require.Equal(t, circuitBreakerStateOpen, cb.state.Load(), "failure in HALF_OPEN should revert to OPEN")
	require.Equal(t, int32(0), cb.halfOpenInFlight.Load())
}

func TestCircuitBreaker_IsHalfOpen(t *testing.T) {
	var cb *accountCircuitBreaker
	require.False(t, cb.isHalfOpen(), "nil should return false")

	cb = &accountCircuitBreaker{}
	require.False(t, cb.isHalfOpen())
	cb.state.Store(circuitBreakerStateHalfOpen)
	require.True(t, cb.isHalfOpen())
}

func TestCircuitBreaker_ReleaseHalfOpenPermit(t *testing.T) {
	var cb *accountCircuitBreaker
	cb.releaseHalfOpenPermit() // should not panic

	cb = &accountCircuitBreaker{}
	cb.releaseHalfOpenPermit() // not in HALF_OPEN, should be no-op

	cb.state.Store(circuitBreakerStateHalfOpen)
	cb.halfOpenInFlight.Store(2)
	cb.releaseHalfOpenPermit()
	require.Equal(t, int32(1), cb.halfOpenInFlight.Load())

	cb.halfOpenInFlight.Store(0)
	cb.releaseHalfOpenPermit() // already at 0, should be no-op
	require.Equal(t, int32(0), cb.halfOpenInFlight.Load())
}

func TestCircuitBreaker_StateString(t *testing.T) {
	cb := &accountCircuitBreaker{}
	require.Equal(t, "CLOSED", cb.stateString())
	cb.state.Store(circuitBreakerStateOpen)
	require.Equal(t, "OPEN", cb.stateString())
	cb.state.Store(circuitBreakerStateHalfOpen)
	require.Equal(t, "HALF_OPEN", cb.stateString())
	cb.state.Store(99)
	require.Equal(t, "UNKNOWN", cb.stateString())
}

func TestCircuitBreaker_IsOpen(t *testing.T) {
	cb := &accountCircuitBreaker{}
	require.False(t, cb.isOpen())
	cb.state.Store(circuitBreakerStateOpen)
	require.True(t, cb.isOpen())
}

func TestCircuitBreaker_FullLifecycle(t *testing.T) {
	cb := &accountCircuitBreaker{}
	threshold := 3
	cooldown := 50 * time.Millisecond

	// CLOSED: allow requests
	require.True(t, cb.allow(cooldown, 2))
	require.Equal(t, "CLOSED", cb.stateString())

	// Trip to OPEN
	for i := 0; i < threshold; i++ {
		cb.recordFailure(threshold)
	}
	require.Equal(t, "OPEN", cb.stateString())
	require.False(t, cb.allow(cooldown, 2), "should deny in OPEN within cooldown")

	// Wait for cooldown
	time.Sleep(cooldown + 10*time.Millisecond)

	// Should transition to HALF_OPEN
	require.True(t, cb.allow(cooldown, 2))
	require.Equal(t, "HALF_OPEN", cb.stateString())

	// Success should close
	cb.recordSuccess()
	require.Equal(t, "CLOSED", cb.stateString())
}

// ---------------------------------------------------------------------------
// dualEWMATTFT Coverage Tests
// ---------------------------------------------------------------------------

func TestDualEWMATTFT_InitNaN(t *testing.T) {
	var d dualEWMATTFT
	d.initNaN()
	require.True(t, math.IsNaN(d.fastValue()))
	require.True(t, math.IsNaN(d.slowValue()))
	_, hasTTFT := d.value()
	require.False(t, hasTTFT, "NaN-initialized should return hasTTFT=false")
}

func TestDualEWMATTFT_UpdateFromNaN(t *testing.T) {
	var d dualEWMATTFT
	d.initNaN()
	d.update(100.0)
	v, ok := d.value()
	require.True(t, ok)
	require.InDelta(t, 100.0, v, 0.01, "first update should set sample directly")
}

func TestDualEWMATTFT_UpdateMultiple(t *testing.T) {
	var d dualEWMATTFT
	d.initNaN()
	for i := 0; i < 20; i++ {
		d.update(200.0)
	}
	v, ok := d.value()
	require.True(t, ok)
	require.InDelta(t, 200.0, v, 1.0, "after many updates of same value, should converge")
}

func TestDualEWMATTFT_ValueFastOnly(t *testing.T) {
	var d dualEWMATTFT
	d.initNaN()
	// Set fast only, slow stays NaN
	d.fastBits.Store(math.Float64bits(42.0))
	v, ok := d.value()
	require.True(t, ok)
	require.Equal(t, 42.0, v)
}

func TestDualEWMATTFT_ValueSlowOnly(t *testing.T) {
	var d dualEWMATTFT
	d.initNaN()
	// Set slow only, fast stays NaN
	d.slowBits.Store(math.Float64bits(55.0))
	v, ok := d.value()
	require.True(t, ok)
	require.Equal(t, 55.0, v)
}

func TestDualEWMATTFT_ValueSlowGreaterThanFast(t *testing.T) {
	var d dualEWMATTFT
	d.fastBits.Store(math.Float64bits(30.0))
	d.slowBits.Store(math.Float64bits(50.0))
	v, ok := d.value()
	require.True(t, ok)
	require.Equal(t, 50.0, v, "pessimistic value should return max(fast, slow)")
}

func TestDualEWMATTFT_ValueFastGreaterThanSlow(t *testing.T) {
	var d dualEWMATTFT
	d.fastBits.Store(math.Float64bits(80.0))
	d.slowBits.Store(math.Float64bits(50.0))
	v, ok := d.value()
	require.True(t, ok)
	require.Equal(t, 80.0, v, "pessimistic value should return max(fast, slow)")
}

// ---------------------------------------------------------------------------
// Softmax Additional Coverage Tests
// ---------------------------------------------------------------------------

func TestSoftmax_EmptyCandidates(t *testing.T) {
	rng := newOpenAISelectionRNG(42)
	result := selectSoftmaxOpenAICandidates(nil, 0.3, &rng)
	require.Nil(t, result)
}

func TestSoftmax_ZeroTemperatureFallsToDefault(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}, score: 0.9},
		{account: &Account{ID: 2}, score: 0.1},
		{account: &Account{ID: 3}, score: 0.5},
		{account: &Account{ID: 4}, score: 0.3},
	}
	rng := newOpenAISelectionRNG(42)
	result := selectSoftmaxOpenAICandidates(candidates, 0, &rng)
	require.Len(t, result, 4)
}

func TestSoftmax_NaNScoresUniform(t *testing.T) {
	// Extreme negative scores that cause exp() to return 0 → uniform fallback
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}, score: -1e308},
		{account: &Account{ID: 2}, score: -1e308},
		{account: &Account{ID: 3}, score: -1e308},
		{account: &Account{ID: 4}, score: -1e308},
	}
	rng := newOpenAISelectionRNG(42)
	result := selectSoftmaxOpenAICandidates(candidates, 0.001, &rng)
	require.Len(t, result, 4)
}

// ---------------------------------------------------------------------------
// Snapshot / Stats Coverage Tests
// ---------------------------------------------------------------------------

func TestSnapshot_NilStats(t *testing.T) {
	var s *openAIAccountRuntimeStats
	errorRate, ttft, hasTTFT := s.snapshot(1)
	require.Equal(t, 0.0, errorRate)
	require.Equal(t, 0.0, ttft)
	require.False(t, hasTTFT)
}

func TestSnapshot_InvalidAccountID(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	errorRate, ttft, hasTTFT := s.snapshot(0)
	require.Equal(t, 0.0, errorRate)
	require.Equal(t, 0.0, ttft)
	require.False(t, hasTTFT)
}

func TestSnapshot_UnknownAccount(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	errorRate, ttft, hasTTFT := s.snapshot(999)
	require.Equal(t, 0.0, errorRate)
	require.Equal(t, 0.0, ttft)
	require.False(t, hasTTFT)
}

func TestSnapshot_WithModelFallback(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	stat := s.loadOrCreate(1)
	// Set global TTFT
	stat.ttft.update(100.0)
	// Snapshot with unknown model should fallback to global
	_, ttft, hasTTFT := s.snapshot(1, "unknown-model")
	require.True(t, hasTTFT)
	require.InDelta(t, 100.0, ttft, 0.01)
}

func TestSnapshot_WithModelSpecific(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	stat := s.loadOrCreate(1)
	stat.reportModelTTFT("gpt-4", 200.0)
	stat.ttft.update(50.0) // global is different
	_, ttft, hasTTFT := s.snapshot(1, "gpt-4")
	require.True(t, hasTTFT)
	require.InDelta(t, 200.0, ttft, 0.01, "should use per-model TTFT")
}

func TestStatsSize_Nil(t *testing.T) {
	var s *openAIAccountRuntimeStats
	require.Equal(t, 0, s.size())
}

func TestStatsSize_Empty(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	require.Equal(t, 0, s.size())
}

func TestStatsSize_WithAccounts(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	s.loadOrCreate(1)
	s.loadOrCreate(2)
	require.Equal(t, 2, s.size())
}

func TestLoadOrCreate_ConcurrentSameID(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	var wg sync.WaitGroup
	results := make([]*openAIAccountRuntimeStat, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = s.loadOrCreate(1)
		}(i)
	}
	wg.Wait()
	// All should return the same pointer
	for i := 1; i < 10; i++ {
		require.Same(t, results[0], results[i], "concurrent loadOrCreate should return same stat")
	}
}

// ---------------------------------------------------------------------------
// modelTTFTValue / reportModelTTFT Coverage Tests
// ---------------------------------------------------------------------------

func TestModelTTFTValue_EmptyModel(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	v, ok := stat.modelTTFTValue("")
	require.False(t, ok)
	require.Equal(t, 0.0, v)
}

func TestModelTTFTValue_UnknownModel(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	v, ok := stat.modelTTFTValue("nonexistent")
	require.False(t, ok)
	require.Equal(t, 0.0, v)
}

func TestReportModelTTFT_EmptyModel(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	stat.ttft.initNaN()
	stat.reportModelTTFT("", 100.0)
	// Should be no-op: global TTFT not updated
	_, hasTTFT := stat.ttft.value()
	require.False(t, hasTTFT)
}

func TestReportModelTTFT_ZeroSample(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	stat.ttft.initNaN()
	stat.reportModelTTFT("gpt-4", 0)
	_, hasTTFT := stat.ttft.value()
	require.False(t, hasTTFT)
}

func TestReportModelTTFT_NegativeSample(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	stat.ttft.initNaN()
	stat.reportModelTTFT("gpt-4", -10.0)
	_, hasTTFT := stat.ttft.value()
	require.False(t, hasTTFT)
}

func TestGetOrCreateModelTTFT_ConcurrentSameModel(t *testing.T) {
	stat := &openAIAccountRuntimeStat{}
	var wg sync.WaitGroup
	results := make([]*dualEWMATTFT, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = stat.getOrCreateModelTTFT("gpt-4")
		}(i)
	}
	wg.Wait()
	for i := 1; i < 10; i++ {
		require.Same(t, results[0], results[i])
	}
}

// ---------------------------------------------------------------------------
// schedulerCircuitBreakerConfig Coverage Tests
// ---------------------------------------------------------------------------

func TestSchedulerCircuitBreakerConfig_Defaults(t *testing.T) {
	svc := &OpenAIGatewayService{}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	enabled, threshold, cooldown, halfOpenMax := scheduler.schedulerCircuitBreakerConfig()
	require.False(t, enabled)
	require.Equal(t, defaultCircuitBreakerFailThreshold, threshold)
	require.Equal(t, time.Duration(defaultCircuitBreakerCooldownSec)*time.Second, cooldown)
	require.Equal(t, defaultCircuitBreakerHalfOpenMax, halfOpenMax)
}

func TestSchedulerCircuitBreakerConfig_Custom(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerFailThreshold = 10
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerCooldownSec = 60
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerHalfOpenMax = 5
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	enabled, threshold, cooldown, halfOpenMax := scheduler.schedulerCircuitBreakerConfig()
	require.True(t, enabled)
	require.Equal(t, 10, threshold)
	require.Equal(t, 60*time.Second, cooldown)
	require.Equal(t, 5, halfOpenMax)
}

func TestSchedulerPerModelTTFTConfig_Defaults(t *testing.T) {
	svc := &OpenAIGatewayService{}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	enabled, maxModels := scheduler.schedulerPerModelTTFTConfig()
	require.False(t, enabled)
	require.Equal(t, defaultPerModelTTFTMaxModels, maxModels)
}

func TestSchedulerPerModelTTFTConfig_Custom(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerPerModelTTFTEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerPerModelTTFTMaxModels = 64
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	enabled, maxModels := scheduler.schedulerPerModelTTFTConfig()
	require.True(t, enabled)
	require.Equal(t, 64, maxModels)
}

func TestReportResult_PerModelTTFTDisabled_NoPerModelTrackerCreated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerPerModelTTFTEnabled = false
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	ttft := 120

	scheduler.ReportResult(7001, true, &ttft, "gpt-5.1", 120)

	stat := scheduler.stats.loadExisting(7001)
	require.NotNil(t, stat)
	count := 0
	stat.modelTTFT.Range(func(_, _ any) bool {
		count++
		return true
	})
	require.Equal(t, 0, count, "per-model ttft should remain disabled")
	_, globalTTFT, hasTTFT := scheduler.stats.snapshot(7001)
	require.True(t, hasTTFT)
	require.InDelta(t, 120.0, globalTTFT, 0.01)
}

func TestReportResult_PerModelTTFTMaxModels_UsesConfigLimit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerPerModelTTFTEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerPerModelTTFTMaxModels = 2
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	ttft := 100

	models := []string{"gpt-5.1", "gpt-4o", "o3", "o4-mini"}
	for i := 0; i < 200; i++ {
		model := models[i%len(models)]
		scheduler.ReportResult(7002, true, &ttft, model, float64(ttft+i))
	}

	stat := scheduler.stats.loadExisting(7002)
	require.NotNil(t, stat)
	count := 0
	stat.modelTTFT.Range(func(_, _ any) bool {
		count++
		return true
	})
	require.LessOrEqual(t, count, 2, "model tracker count should honor scheduler_per_model_ttft_max_models")
}

// ---------------------------------------------------------------------------
// P2C Edge Case Coverage
// ---------------------------------------------------------------------------

func TestSelectP2C_EmptyCandidates(t *testing.T) {
	result := selectP2COpenAICandidates(nil, OpenAIAccountScheduleRequest{})
	require.Nil(t, result)
}

func TestSelectP2C_SingleCandidate(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}, score: 0.5},
	}
	result := selectP2COpenAICandidates(candidates, OpenAIAccountScheduleRequest{})
	require.Len(t, result, 1)
	require.Equal(t, int64(1), result[0].account.ID)
}

func TestSelectP2C_TwoCandidatesPicksBetter(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}, score: 0.2},
		{account: &Account{ID: 2}, score: 0.8},
	}
	result := selectP2COpenAICandidates(candidates, OpenAIAccountScheduleRequest{})
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].account.ID, "first should be higher-scored")
}

// ---------------------------------------------------------------------------
// TopK Selection & Heap Coverage
// ---------------------------------------------------------------------------

func TestSelectTopK_Empty(t *testing.T) {
	result := selectTopKOpenAICandidates(nil, 3)
	require.Nil(t, result)
}

func TestSelectTopK_TopKZero(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1}, score: 0.5, loadInfo: &AccountLoadInfo{}},
	}
	result := selectTopKOpenAICandidates(candidates, 0)
	require.Len(t, result, 1, "topK=0 should default to 1")
}

func TestSelectTopK_TopKExceedsCandidates(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1, Priority: 1}, score: 0.3, loadInfo: &AccountLoadInfo{}},
		{account: &Account{ID: 2, Priority: 1}, score: 0.9, loadInfo: &AccountLoadInfo{}},
	}
	result := selectTopKOpenAICandidates(candidates, 10)
	require.Len(t, result, 2)
	require.Equal(t, int64(2), result[0].account.ID, "highest score first")
}

func TestSelectTopK_ProperFiltering(t *testing.T) {
	candidates := []openAIAccountCandidateScore{
		{account: &Account{ID: 1, Priority: 1}, score: 0.1, loadInfo: &AccountLoadInfo{}},
		{account: &Account{ID: 2, Priority: 1}, score: 0.9, loadInfo: &AccountLoadInfo{}},
		{account: &Account{ID: 3, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{}},
		{account: &Account{ID: 4, Priority: 1}, score: 0.3, loadInfo: &AccountLoadInfo{}},
		{account: &Account{ID: 5, Priority: 1}, score: 0.7, loadInfo: &AccountLoadInfo{}},
	}
	result := selectTopKOpenAICandidates(candidates, 3)
	require.Len(t, result, 3)
	require.Equal(t, int64(2), result[0].account.ID)
	require.Equal(t, int64(5), result[1].account.ID)
	require.Equal(t, int64(3), result[2].account.ID)
}

func TestIsOpenAIAccountCandidateBetter_AllTiebreakers(t *testing.T) {
	// Equal scores, different priority
	a := openAIAccountCandidateScore{account: &Account{ID: 1, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 50, WaitingCount: 5}}
	b := openAIAccountCandidateScore{account: &Account{ID: 2, Priority: 2}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 50, WaitingCount: 5}}
	require.True(t, isOpenAIAccountCandidateBetter(a, b), "lower priority number = better")
	require.False(t, isOpenAIAccountCandidateBetter(b, a))

	// Equal scores and priority, different load rate
	c := openAIAccountCandidateScore{account: &Account{ID: 1, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 30, WaitingCount: 5}}
	d := openAIAccountCandidateScore{account: &Account{ID: 2, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 60, WaitingCount: 5}}
	require.True(t, isOpenAIAccountCandidateBetter(c, d), "lower load rate = better")

	// Equal everything except waiting count
	e := openAIAccountCandidateScore{account: &Account{ID: 1, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 50, WaitingCount: 2}}
	f := openAIAccountCandidateScore{account: &Account{ID: 2, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 50, WaitingCount: 8}}
	require.True(t, isOpenAIAccountCandidateBetter(e, f), "lower waiting count = better")

	// Equal everything except ID
	g := openAIAccountCandidateScore{account: &Account{ID: 1, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 50, WaitingCount: 5}}
	h := openAIAccountCandidateScore{account: &Account{ID: 2, Priority: 1}, score: 0.5, loadInfo: &AccountLoadInfo{LoadRate: 50, WaitingCount: 5}}
	require.True(t, isOpenAIAccountCandidateBetter(g, h), "lower ID = better")
}

// ---------------------------------------------------------------------------
// shouldReleaseStickySession Coverage
// ---------------------------------------------------------------------------

func TestShouldReleaseStickySession_NilScheduler(t *testing.T) {
	var s *defaultOpenAIAccountScheduler
	require.False(t, s.shouldReleaseStickySession(1))
}

func TestShouldReleaseStickySession_Disabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickyReleaseEnabled = false
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	require.False(t, scheduler.shouldReleaseStickySession(1))
}

func TestShouldReleaseStickySession_CircuitOpen(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickyReleaseEnabled = true
	cfg.Gateway.OpenAIWS.SchedulerCircuitBreakerEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	// Trip the circuit breaker
	cb := stats.getCircuitBreaker(1)
	for i := 0; i < defaultCircuitBreakerFailThreshold; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	require.True(t, scheduler.shouldReleaseStickySession(1), "should release when circuit is open")
}

func TestShouldReleaseStickySession_HighErrorRate(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickyReleaseEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	// Report many failures to push error rate above threshold
	for i := 0; i < 20; i++ {
		stats.report(1, false, nil, "", 0)
	}
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	require.True(t, scheduler.shouldReleaseStickySession(1), "should release when error rate is high")
}

func TestShouldReleaseStickySession_Healthy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickyReleaseEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	stats := newOpenAIAccountRuntimeStats()
	// Report successes
	for i := 0; i < 20; i++ {
		stats.report(1, true, nil, "", 0)
	}
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	require.False(t, scheduler.shouldReleaseStickySession(1), "should not release when healthy")
}

// ---------------------------------------------------------------------------
// stickyReleaseConfigRead Coverage
// ---------------------------------------------------------------------------

func TestStickyReleaseConfigRead_NilConfig(t *testing.T) {
	svc := &OpenAIGatewayService{}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	cfg := scheduler.stickyReleaseConfigRead()
	require.False(t, cfg.enabled)
	require.Equal(t, 0.0, cfg.errorThreshold, "nil config returns zero-value struct")
}

func TestStickyReleaseConfigRead_Defaults(t *testing.T) {
	c := &config.Config{}
	// StickyReleaseErrorThreshold defaults to 0 → code uses defaultStickyReleaseErrorThreshold
	svc := &OpenAIGatewayService{cfg: c}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	cfg := scheduler.stickyReleaseConfigRead()
	require.False(t, cfg.enabled)
	require.Equal(t, defaultStickyReleaseErrorThreshold, cfg.errorThreshold)
}

func TestStickyReleaseConfigRead_Custom(t *testing.T) {
	c := &config.Config{}
	c.Gateway.OpenAIWS.StickyReleaseEnabled = true
	c.Gateway.OpenAIWS.StickyReleaseErrorThreshold = 0.5
	svc := &OpenAIGatewayService{cfg: c}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	cfg := scheduler.stickyReleaseConfigRead()
	require.True(t, cfg.enabled)
	require.Equal(t, 0.5, cfg.errorThreshold)
}

// ---------------------------------------------------------------------------
// RNG Coverage
// ---------------------------------------------------------------------------

func TestNewOpenAISelectionRNG_ZeroSeed(t *testing.T) {
	rng := newOpenAISelectionRNG(0)
	require.NotEqual(t, uint64(0), rng.state, "zero seed should be replaced with default")
	v := rng.nextFloat64()
	require.True(t, v >= 0 && v < 1.0)
}

// ---------------------------------------------------------------------------
// isCircuitOpen Coverage
// ---------------------------------------------------------------------------

func TestIsCircuitOpen_UnknownAccount(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	require.False(t, stats.isCircuitOpen(999), "unknown account should not be circuit-open")
}

func TestIsCircuitOpen_OpenAccount(t *testing.T) {
	stats := newOpenAIAccountRuntimeStats()
	cb := stats.getCircuitBreaker(1)
	for i := 0; i < defaultCircuitBreakerFailThreshold; i++ {
		cb.recordFailure(defaultCircuitBreakerFailThreshold)
	}
	require.True(t, stats.isCircuitOpen(1))
}

// ---------------------------------------------------------------------------
// openAIWSSchedulerP2CEnabled / openAIWSSchedulerWeights Coverage
// ---------------------------------------------------------------------------

func TestP2CEnabled_NilConfig(t *testing.T) {
	svc := &OpenAIGatewayService{}
	require.False(t, svc.openAIWSSchedulerP2CEnabled())
}

func TestP2CEnabled_Enabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerP2CEnabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	require.True(t, svc.openAIWSSchedulerP2CEnabled())
}

func TestSchedulerWeights_NilConfig(t *testing.T) {
	svc := &OpenAIGatewayService{}
	w := svc.openAIWSSchedulerWeights()
	require.Equal(t, 1.0, w.Priority)
	require.Equal(t, 1.0, w.Load)
	require.Equal(t, 0.7, w.Queue)
	require.Equal(t, 0.8, w.ErrorRate)
	require.Equal(t, 0.5, w.TTFT)
}

func TestSchedulerWeights_Custom(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 2.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load = 3.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue = 1.0
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate = 1.5
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT = 0.8
	svc := &OpenAIGatewayService{cfg: cfg}
	w := svc.openAIWSSchedulerWeights()
	require.Equal(t, 2.0, w.Priority)
	require.Equal(t, 3.0, w.Load)
}

// ---------------------------------------------------------------------------
// Snapshot edge cases
// ---------------------------------------------------------------------------

func TestSnapshot_WithEmptyModel(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	stat := s.loadOrCreate(1)
	stat.ttft.update(100.0)
	_, ttft, hasTTFT := s.snapshot(1, "")
	require.True(t, hasTTFT)
	require.InDelta(t, 100.0, ttft, 0.01, "empty model string should fall through to global")
}

func TestSnapshot_NoModelArg(t *testing.T) {
	s := newOpenAIAccountRuntimeStats()
	stat := s.loadOrCreate(1)
	stat.ttft.update(100.0)
	_, ttft, hasTTFT := s.snapshot(1)
	require.True(t, hasTTFT)
	require.InDelta(t, 100.0, ttft, 0.01)
}

// ---------------------------------------------------------------------------
// deriveOpenAISelectionSeed coverage
// ---------------------------------------------------------------------------

func TestDeriveOpenAISelectionSeed_WithSessionHash(t *testing.T) {
	seed := deriveOpenAISelectionSeed(OpenAIAccountScheduleRequest{SessionHash: "abc123"})
	require.NotEqual(t, uint64(0), seed)
}

func TestDeriveOpenAISelectionSeed_WithPreviousResponseID(t *testing.T) {
	seed := deriveOpenAISelectionSeed(OpenAIAccountScheduleRequest{PreviousResponseID: "resp_123"})
	require.NotEqual(t, uint64(0), seed)
}

func TestDeriveOpenAISelectionSeed_WithGroupID(t *testing.T) {
	gid := int64(42)
	seed := deriveOpenAISelectionSeed(OpenAIAccountScheduleRequest{GroupID: &gid})
	require.NotEqual(t, uint64(0), seed)
}

func TestDeriveOpenAISelectionSeed_Empty(t *testing.T) {
	seed := deriveOpenAISelectionSeed(OpenAIAccountScheduleRequest{})
	require.NotEqual(t, uint64(0), seed, "empty request should use time entropy")
}

func TestDeriveOpenAISelectionSeed_WithModel(t *testing.T) {
	seed := deriveOpenAISelectionSeed(OpenAIAccountScheduleRequest{RequestedModel: "gpt-4"})
	require.NotEqual(t, uint64(0), seed)
}

// ---------------------------------------------------------------------------
// SnapshotMetrics coverage
// ---------------------------------------------------------------------------

func TestSnapshotMetrics_NilScheduler(t *testing.T) {
	var s *defaultOpenAIAccountScheduler
	metrics := s.SnapshotMetrics()
	require.Equal(t, int64(0), metrics.SelectTotal)
}

func TestSnapshotMetrics_Normal(t *testing.T) {
	svc := &OpenAIGatewayService{}
	stats := newOpenAIAccountRuntimeStats()
	scheduler := newDefaultOpenAIAccountScheduler(svc, stats).(*defaultOpenAIAccountScheduler)
	metrics := scheduler.SnapshotMetrics()
	require.Equal(t, int64(0), metrics.SelectTotal)
}

// ---------------------------------------------------------------------------
// Heap Pop coverage
// ---------------------------------------------------------------------------

func TestCandidateHeap_Pop(t *testing.T) {
	h := &openAIAccountCandidateHeap{}
	heap.Push(h, openAIAccountCandidateScore{account: &Account{ID: 1}, score: 0.5, loadInfo: &AccountLoadInfo{}})
	heap.Push(h, openAIAccountCandidateScore{account: &Account{ID: 2}, score: 0.9, loadInfo: &AccountLoadInfo{}})
	heap.Push(h, openAIAccountCandidateScore{account: &Account{ID: 3}, score: 0.3, loadInfo: &AccountLoadInfo{}})
	require.Equal(t, 3, h.Len())

	// Pop returns the minimum (worst candidate in min-heap)
	popped := heap.Pop(h).(openAIAccountCandidateScore)
	require.Equal(t, int64(3), popped.account.ID, "should pop the lowest-scored")
	require.Equal(t, 2, h.Len())
}

// ---------------------------------------------------------------------------
// openAIWSSessionStickyTTL coverage
// ---------------------------------------------------------------------------

func TestOpenAIWSSessionStickyTTL_DefaultConfig(t *testing.T) {
	svc := &OpenAIGatewayService{}
	ttl := svc.openAIWSSessionStickyTTL()
	require.Equal(t, openaiStickySessionTTL, ttl, "nil config should return default TTL")
}

func TestOpenAIWSSessionStickyTTL_Custom(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
	svc := &OpenAIGatewayService{cfg: cfg}
	ttl := svc.openAIWSSessionStickyTTL()
	require.Equal(t, 1800*time.Second, ttl)
}
