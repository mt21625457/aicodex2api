package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SoraClientHandler 处理 Sora 客户端 API 请求。
type SoraClientHandler struct {
	genService         *service.SoraGenerationService
	quotaService       *service.SoraQuotaService
	s3Storage          *service.SoraS3Storage
	soraGatewayService *service.SoraGatewayService
	gatewayService     *service.GatewayService
	mediaStorage       *service.SoraMediaStorage
}

// NewSoraClientHandler 创建 Sora 客户端 Handler。
func NewSoraClientHandler(
	genService *service.SoraGenerationService,
	quotaService *service.SoraQuotaService,
	s3Storage *service.SoraS3Storage,
	soraGatewayService *service.SoraGatewayService,
	gatewayService *service.GatewayService,
	mediaStorage *service.SoraMediaStorage,
) *SoraClientHandler {
	return &SoraClientHandler{
		genService:         genService,
		quotaService:       quotaService,
		s3Storage:          s3Storage,
		soraGatewayService: soraGatewayService,
		gatewayService:     gatewayService,
		mediaStorage:       mediaStorage,
	}
}

// GenerateRequest 生成请求。
type GenerateRequest struct {
	Model      string `json:"model" binding:"required"`
	Prompt     string `json:"prompt" binding:"required"`
	MediaType  string `json:"media_type"`            // video / image，默认 video
	ImageInput string `json:"image_input,omitempty"` // 参考图（base64 或 URL）
}

// Generate 异步生成 — 创建 pending 记录后立即返回。
// POST /api/v1/sora/generate
func (h *SoraClientHandler) Generate(c *gin.Context) {
	userID := getUserIDFromContext(c)
	if userID == 0 {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	var req GenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	if req.MediaType == "" {
		req.MediaType = "video"
	}

	// 并发数检查（最多 3 个）
	activeCount, err := h.genService.CountActiveByUser(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if activeCount >= 3 {
		response.Error(c, http.StatusTooManyRequests, "同时进行中的任务不能超过 3 个")
		return
	}

	// 配额检查（粗略检查，实际文件大小在上传后才知道）
	if h.quotaService != nil {
		if err := h.quotaService.CheckQuota(c.Request.Context(), userID, 0); err != nil {
			var quotaErr *service.QuotaExceededError
			if errors.As(err, &quotaErr) {
				response.Error(c, http.StatusTooManyRequests, "存储配额已满，请删除不需要的作品释放空间")
				return
			}
			response.Error(c, http.StatusForbidden, err.Error())
			return
		}
	}

	// 获取 API Key ID（如果是通过 API Key 认证）
	var apiKeyID *int64
	if id, ok := c.Get("api_key_id"); ok {
		if v, ok := id.(int64); ok {
			apiKeyID = &v
		}
	}

	gen, err := h.genService.CreatePending(c.Request.Context(), userID, apiKeyID, req.Model, req.Prompt, req.MediaType)
	if err != nil {
		if errors.Is(err, service.ErrSoraGenerationConcurrencyLimit) {
			response.Error(c, http.StatusTooManyRequests, "同时进行中的任务不能超过 3 个")
			return
		}
		response.ErrorFrom(c, err)
		return
	}

	// 启动后台异步生成 goroutine
	go h.processGeneration(gen.ID, userID, req.Model, req.Prompt, req.MediaType, req.ImageInput)

	response.Success(c, gin.H{
		"generation_id": gen.ID,
		"status":        gen.Status,
	})
}

// processGeneration 后台异步执行 Sora 生成任务。
// 流程：选择账号 → Forward → 提取媒体 URL → 三层降级存储（S3 → 本地 → 上游）→ 更新记录。
func (h *SoraClientHandler) processGeneration(genID int64, userID int64, model, prompt, mediaType, imageInput string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 标记为生成中
	if err := h.genService.MarkGenerating(ctx, genID, ""); err != nil {
		if errors.Is(err, service.ErrSoraGenerationStateConflict) {
			logger.LegacyPrintf("handler.sora_client", "[SoraClient] 任务状态已变化，跳过生成 id=%d", genID)
			return
		}
		logger.LegacyPrintf("handler.sora_client", "[SoraClient] 标记生成中失败 id=%d err=%v", genID, err)
		return
	}

	// 设置 ForcePlatform 用于 Sora 账号选择
	ctx = context.WithValue(ctx, ctxkey.ForcePlatform, service.PlatformSora)

	if h.gatewayService == nil {
		_ = h.genService.MarkFailed(ctx, genID, "内部错误: gatewayService 未初始化")
		return
	}

	// 选择 Sora 账号
	account, err := h.gatewayService.SelectAccountForModel(ctx, nil, "", model)
	if err != nil {
		_ = h.genService.MarkFailed(ctx, genID, "选择账号失败: "+err.Error())
		return
	}

	// 构建 chat completions 请求体（非流式）
	body := buildAsyncRequestBody(model, prompt, imageInput)

	if h.soraGatewayService == nil {
		_ = h.genService.MarkFailed(ctx, genID, "内部错误: soraGatewayService 未初始化")
		return
	}

	// 创建 mock gin 上下文用于 Forward（捕获响应以提取媒体 URL）
	recorder := httptest.NewRecorder()
	mockGinCtx, _ := gin.CreateTestContext(recorder)
	mockGinCtx.Request, _ = http.NewRequest("POST", "/", nil)

	// 调用 Forward（非流式）
	result, err := h.soraGatewayService.Forward(ctx, mockGinCtx, account, body, false)
	if err != nil {
		// 检查是否已取消
		gen, _ := h.genService.GetByID(ctx, genID, userID)
		if gen != nil && gen.Status == service.SoraGenStatusCancelled {
			return
		}
		_ = h.genService.MarkFailed(ctx, genID, "生成失败: "+err.Error())
		return
	}

	// 提取媒体 URL（优先从 ForwardResult，其次从响应体解析）
	mediaURL, mediaURLs := extractMediaURLsFromResult(result, recorder)
	if mediaURL == "" {
		_ = h.genService.MarkFailed(ctx, genID, "未获取到媒体 URL")
		return
	}

	// 检查任务是否已被取消
	gen, _ := h.genService.GetByID(ctx, genID, userID)
	if gen != nil && gen.Status == service.SoraGenStatusCancelled {
		logger.LegacyPrintf("handler.sora_client", "[SoraClient] 任务已取消，跳过存储 id=%d", genID)
		return
	}

	// 三层降级存储：S3 → 本地 → 上游临时 URL
	storedURL, storedURLs, storageType, s3Keys, fileSize := h.storeMediaWithDegradation(ctx, userID, mediaType, mediaURL, mediaURLs)

	usageAdded := false
	if (storageType == service.SoraStorageTypeS3 || storageType == service.SoraStorageTypeLocal) && fileSize > 0 && h.quotaService != nil {
		if err := h.quotaService.AddUsage(ctx, userID, fileSize); err != nil {
			h.cleanupStoredMedia(ctx, storageType, s3Keys, storedURLs)
			var quotaErr *service.QuotaExceededError
			if errors.As(err, &quotaErr) {
				_ = h.genService.MarkFailed(ctx, genID, "存储配额已满，请删除不需要的作品释放空间")
				return
			}
			_ = h.genService.MarkFailed(ctx, genID, "存储配额更新失败: "+err.Error())
			return
		}
		usageAdded = true
	}

	// 存储完成后再做一次取消检查，防止取消被 completed 覆盖。
	gen, _ = h.genService.GetByID(ctx, genID, userID)
	if gen != nil && gen.Status == service.SoraGenStatusCancelled {
		logger.LegacyPrintf("handler.sora_client", "[SoraClient] 存储后检测到任务已取消，回滚存储 id=%d", genID)
		h.cleanupStoredMedia(ctx, storageType, s3Keys, storedURLs)
		if usageAdded && h.quotaService != nil {
			_ = h.quotaService.ReleaseUsage(ctx, userID, fileSize)
		}
		return
	}

	// 标记完成
	if err := h.genService.MarkCompleted(ctx, genID, storedURL, storedURLs, storageType, s3Keys, fileSize); err != nil {
		if errors.Is(err, service.ErrSoraGenerationStateConflict) {
			h.cleanupStoredMedia(ctx, storageType, s3Keys, storedURLs)
			if usageAdded && h.quotaService != nil {
				_ = h.quotaService.ReleaseUsage(ctx, userID, fileSize)
			}
			return
		}
		logger.LegacyPrintf("handler.sora_client", "[SoraClient] 标记完成失败 id=%d err=%v", genID, err)
		return
	}

	logger.LegacyPrintf("handler.sora_client", "[SoraClient] 生成完成 id=%d storage=%s size=%d", genID, storageType, fileSize)
}

// storeMediaWithDegradation 实现三层降级存储链：S3 → 本地 → 上游。
func (h *SoraClientHandler) storeMediaWithDegradation(
	ctx context.Context, userID int64, mediaType string,
	mediaURL string, mediaURLs []string,
) (storedURL string, storedURLs []string, storageType string, s3Keys []string, fileSize int64) {
	urls := mediaURLs
	if len(urls) == 0 {
		urls = []string{mediaURL}
	}

	// 第一层：尝试 S3
	if h.s3Storage != nil && h.s3Storage.Enabled(ctx) {
		keys := make([]string, 0, len(urls))
		var totalSize int64
		allOK := true
		for _, u := range urls {
			key, size, err := h.s3Storage.UploadFromURL(ctx, userID, u)
			if err != nil {
				logger.LegacyPrintf("handler.sora_client", "[SoraClient] S3 上传失败 err=%v", err)
				allOK = false
				// 清理已上传的文件
				if len(keys) > 0 {
					_ = h.s3Storage.DeleteObjects(ctx, keys)
				}
				break
			}
			keys = append(keys, key)
			totalSize += size
		}
		if allOK && len(keys) > 0 {
			accessURLs := make([]string, 0, len(keys))
			for _, key := range keys {
				accessURL, err := h.s3Storage.GetAccessURL(ctx, key)
				if err != nil {
					logger.LegacyPrintf("handler.sora_client", "[SoraClient] 生成 S3 访问 URL 失败 err=%v", err)
					_ = h.s3Storage.DeleteObjects(ctx, keys)
					allOK = false
					break
				}
				accessURLs = append(accessURLs, accessURL)
			}
			if allOK && len(accessURLs) > 0 {
				return accessURLs[0], accessURLs, service.SoraStorageTypeS3, keys, totalSize
			}
		}
	}

	// 第二层：尝试本地存储
	if h.mediaStorage != nil && h.mediaStorage.Enabled() {
		storedPaths, err := h.mediaStorage.StoreFromURLs(ctx, mediaType, urls)
		if err == nil && len(storedPaths) > 0 {
			firstPath := storedPaths[0]
			totalSize, sizeErr := h.mediaStorage.TotalSizeByRelativePaths(storedPaths)
			if sizeErr != nil {
				logger.LegacyPrintf("handler.sora_client", "[SoraClient] 统计本地文件大小失败 err=%v", sizeErr)
			}
			return firstPath, storedPaths, service.SoraStorageTypeLocal, nil, totalSize
		}
		logger.LegacyPrintf("handler.sora_client", "[SoraClient] 本地存储失败 err=%v", err)
	}

	// 第三层：保留上游临时 URL
	return urls[0], urls, service.SoraStorageTypeUpstream, nil, 0
}

// buildAsyncRequestBody 构建 Sora 异步生成的 chat completions 请求体。
func buildAsyncRequestBody(model, prompt, imageInput string) []byte {
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"stream": false,
	}
	if imageInput != "" {
		body["image_input"] = imageInput
	}
	b, _ := json.Marshal(body)
	return b
}

// extractMediaURLsFromResult 从 Forward 结果和响应体中提取媒体 URL。
// OAuth 路径：ForwardResult.MediaURL 已填充。
// APIKey 路径：需从响应体解析 media_url / media_urls 字段。
func extractMediaURLsFromResult(result *service.ForwardResult, recorder *httptest.ResponseRecorder) (string, []string) {
	// 优先从 ForwardResult 获取（OAuth 路径）
	if result != nil && result.MediaURL != "" {
		// 尝试从响应体获取完整 URL 列表
		if urls := parseMediaURLsFromBody(recorder.Body.Bytes()); len(urls) > 0 {
			return urls[0], urls
		}
		return result.MediaURL, []string{result.MediaURL}
	}

	// 从响应体解析（APIKey 路径）
	if urls := parseMediaURLsFromBody(recorder.Body.Bytes()); len(urls) > 0 {
		return urls[0], urls
	}

	return "", nil
}

// parseMediaURLsFromBody 从 JSON 响应体中解析 media_url / media_urls 字段。
func parseMediaURLsFromBody(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}

	// 优先 media_urls（多图数组）
	if rawURLs, ok := resp["media_urls"]; ok {
		if arr, ok := rawURLs.([]any); ok && len(arr) > 0 {
			urls := make([]string, 0, len(arr))
			for _, item := range arr {
				if s, ok := item.(string); ok && s != "" {
					urls = append(urls, s)
				}
			}
			if len(urls) > 0 {
				return urls
			}
		}
	}

	// 回退到 media_url（单个 URL）
	if url, ok := resp["media_url"].(string); ok && url != "" {
		return []string{url}
	}

	return nil
}

// ListGenerations 查询生成记录列表。
// GET /api/v1/sora/generations
func (h *SoraClientHandler) ListGenerations(c *gin.Context) {
	userID := getUserIDFromContext(c)
	if userID == 0 {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	params := service.SoraGenerationListParams{
		UserID:      userID,
		Status:      c.Query("status"),
		StorageType: c.Query("storage_type"),
		MediaType:   c.Query("media_type"),
		Page:        page,
		PageSize:    pageSize,
	}

	gens, total, err := h.genService.List(c.Request.Context(), params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	// 为 S3 记录动态生成预签名 URL
	for _, gen := range gens {
		_ = h.genService.ResolveMediaURLs(c.Request.Context(), gen)
	}

	response.Success(c, gin.H{
		"data":  gens,
		"total": total,
		"page":  page,
	})
}

// GetGeneration 查询生成记录详情。
// GET /api/v1/sora/generations/:id
func (h *SoraClientHandler) GetGeneration(c *gin.Context) {
	userID := getUserIDFromContext(c)
	if userID == 0 {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "无效的 ID")
		return
	}

	gen, err := h.genService.GetByID(c.Request.Context(), id, userID)
	if err != nil {
		response.Error(c, http.StatusNotFound, err.Error())
		return
	}

	_ = h.genService.ResolveMediaURLs(c.Request.Context(), gen)
	response.Success(c, gen)
}

// DeleteGeneration 删除生成记录。
// DELETE /api/v1/sora/generations/:id
func (h *SoraClientHandler) DeleteGeneration(c *gin.Context) {
	userID := getUserIDFromContext(c)
	if userID == 0 {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "无效的 ID")
		return
	}

	gen, err := h.genService.GetByID(c.Request.Context(), id, userID)
	if err != nil {
		response.Error(c, http.StatusNotFound, err.Error())
		return
	}

	// 先尝试清理本地文件，再删除记录（清理失败不阻塞删除）。
	if gen.StorageType == service.SoraStorageTypeLocal && h.mediaStorage != nil {
		paths := gen.MediaURLs
		if len(paths) == 0 && gen.MediaURL != "" {
			paths = []string{gen.MediaURL}
		}
		if err := h.mediaStorage.DeleteByRelativePaths(paths); err != nil {
			logger.LegacyPrintf("handler.sora_client", "[SoraClient] 删除本地文件失败 id=%d err=%v", id, err)
		}
	}

	if err := h.genService.Delete(c.Request.Context(), id, userID); err != nil {
		response.Error(c, http.StatusNotFound, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "已删除"})
}

// GetQuota 查询用户存储配额。
// GET /api/v1/sora/quota
func (h *SoraClientHandler) GetQuota(c *gin.Context) {
	userID := getUserIDFromContext(c)
	if userID == 0 {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	if h.quotaService == nil {
		response.Success(c, service.QuotaInfo{QuotaSource: "unlimited", Source: "unlimited"})
		return
	}

	quota, err := h.quotaService.GetQuota(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, quota)
}

// CancelGeneration 取消生成任务。
// POST /api/v1/sora/generations/:id/cancel
func (h *SoraClientHandler) CancelGeneration(c *gin.Context) {
	userID := getUserIDFromContext(c)
	if userID == 0 {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "无效的 ID")
		return
	}

	// 权限校验
	gen, err := h.genService.GetByID(c.Request.Context(), id, userID)
	if err != nil {
		response.Error(c, http.StatusNotFound, err.Error())
		return
	}
	_ = gen

	if err := h.genService.MarkCancelled(c.Request.Context(), id); err != nil {
		if errors.Is(err, service.ErrSoraGenerationNotActive) {
			response.Error(c, http.StatusConflict, "任务已结束，无法取消")
			return
		}
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}

	response.Success(c, gin.H{"message": "已取消"})
}

// SaveToStorage 手动保存 upstream 记录到 S3。
// POST /api/v1/sora/generations/:id/save
func (h *SoraClientHandler) SaveToStorage(c *gin.Context) {
	userID := getUserIDFromContext(c)
	if userID == 0 {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "无效的 ID")
		return
	}

	gen, err := h.genService.GetByID(c.Request.Context(), id, userID)
	if err != nil {
		response.Error(c, http.StatusNotFound, err.Error())
		return
	}

	if gen.StorageType != service.SoraStorageTypeUpstream {
		response.Error(c, http.StatusBadRequest, "仅 upstream 类型的记录可手动保存")
		return
	}
	if gen.MediaURL == "" {
		response.Error(c, http.StatusBadRequest, "媒体 URL 为空，可能已过期")
		return
	}

	if h.s3Storage == nil || !h.s3Storage.Enabled(c.Request.Context()) {
		response.Error(c, http.StatusServiceUnavailable, "云存储未配置，请联系管理员")
		return
	}

	sourceURLs := gen.MediaURLs
	if len(sourceURLs) == 0 && gen.MediaURL != "" {
		sourceURLs = []string{gen.MediaURL}
	}
	if len(sourceURLs) == 0 {
		response.Error(c, http.StatusBadRequest, "媒体 URL 为空，可能已过期")
		return
	}

	uploadedKeys := make([]string, 0, len(sourceURLs))
	accessURLs := make([]string, 0, len(sourceURLs))
	var totalSize int64

	for _, sourceURL := range sourceURLs {
		objectKey, fileSize, uploadErr := h.s3Storage.UploadFromURL(c.Request.Context(), userID, sourceURL)
		if uploadErr != nil {
			if len(uploadedKeys) > 0 {
				_ = h.s3Storage.DeleteObjects(c.Request.Context(), uploadedKeys)
			}
			var upstreamErr *service.UpstreamDownloadError
			if errors.As(uploadErr, &upstreamErr) && (upstreamErr.StatusCode == http.StatusForbidden || upstreamErr.StatusCode == http.StatusNotFound) {
				response.Error(c, http.StatusGone, "媒体链接已过期，无法保存")
				return
			}
			response.Error(c, http.StatusInternalServerError, "上传到 S3 失败: "+uploadErr.Error())
			return
		}
		accessURL, err := h.s3Storage.GetAccessURL(c.Request.Context(), objectKey)
		if err != nil {
			uploadedKeys = append(uploadedKeys, objectKey)
			_ = h.s3Storage.DeleteObjects(c.Request.Context(), uploadedKeys)
			response.Error(c, http.StatusInternalServerError, "生成 S3 访问链接失败: "+err.Error())
			return
		}
		uploadedKeys = append(uploadedKeys, objectKey)
		accessURLs = append(accessURLs, accessURL)
		totalSize += fileSize
	}

	usageAdded := false
	if totalSize > 0 && h.quotaService != nil {
		if err := h.quotaService.AddUsage(c.Request.Context(), userID, totalSize); err != nil {
			_ = h.s3Storage.DeleteObjects(c.Request.Context(), uploadedKeys)
			var quotaErr *service.QuotaExceededError
			if errors.As(err, &quotaErr) {
				response.Error(c, http.StatusTooManyRequests, "存储配额已满，请删除不需要的作品释放空间")
				return
			}
			response.Error(c, http.StatusInternalServerError, "配额更新失败: "+err.Error())
			return
		}
		usageAdded = true
	}

	if err := h.genService.UpdateStorageForCompleted(
		c.Request.Context(),
		id,
		accessURLs[0],
		accessURLs,
		service.SoraStorageTypeS3,
		uploadedKeys,
		totalSize,
	); err != nil {
		_ = h.s3Storage.DeleteObjects(c.Request.Context(), uploadedKeys)
		if usageAdded && h.quotaService != nil {
			_ = h.quotaService.ReleaseUsage(c.Request.Context(), userID, totalSize)
		}
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"message":     "已保存到 S3",
		"object_key":  uploadedKeys[0],
		"object_keys": uploadedKeys,
	})
}

// GetStorageStatus 返回存储状态。
// GET /api/v1/sora/storage-status
func (h *SoraClientHandler) GetStorageStatus(c *gin.Context) {
	s3Enabled := h.s3Storage != nil && h.s3Storage.Enabled(c.Request.Context())
	s3Healthy := false
	if s3Enabled {
		s3Healthy = h.s3Storage.IsHealthy(c.Request.Context())
	}
	localEnabled := h.mediaStorage != nil && h.mediaStorage.Enabled()
	response.Success(c, gin.H{
		"s3_enabled":    s3Enabled,
		"s3_healthy":    s3Healthy,
		"local_enabled": localEnabled,
	})
}

func (h *SoraClientHandler) cleanupStoredMedia(ctx context.Context, storageType string, s3Keys []string, localPaths []string) {
	switch storageType {
	case service.SoraStorageTypeS3:
		if h.s3Storage != nil && len(s3Keys) > 0 {
			if err := h.s3Storage.DeleteObjects(ctx, s3Keys); err != nil {
				logger.LegacyPrintf("handler.sora_client", "[SoraClient] 清理 S3 文件失败 keys=%v err=%v", s3Keys, err)
			}
		}
	case service.SoraStorageTypeLocal:
		if h.mediaStorage != nil && len(localPaths) > 0 {
			if err := h.mediaStorage.DeleteByRelativePaths(localPaths); err != nil {
				logger.LegacyPrintf("handler.sora_client", "[SoraClient] 清理本地文件失败 paths=%v err=%v", localPaths, err)
			}
		}
	}
}

// getUserIDFromContext 从 gin 上下文中提取用户 ID。
func getUserIDFromContext(c *gin.Context) int64 {
	if id, ok := c.Get("user_id"); ok {
		switch v := id.(type) {
		case int64:
			return v
		case float64:
			return int64(v)
		case string:
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	// 尝试从 JWT claims 获取
	if id, ok := c.Get("userID"); ok {
		if v, ok := id.(int64); ok {
			return v
		}
	}
	return 0
}

// GetModels 获取可用 Sora 模型家族列表（从 soraModelConfigs 自动聚合）。
// GET /api/v1/sora/models
func (h *SoraClientHandler) GetModels(c *gin.Context) {
	families := service.BuildSoraModelFamilies()
	response.Success(c, families)
}
