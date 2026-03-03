package admin

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	"github.com/gin-gonic/gin"
)

// GetOpenAIWSV2PassthroughMetrics returns OpenAI WS v2 passthrough runtime metrics.
// GET /api/v1/admin/ops/openai-ws-v2/passthrough-metrics
func (h *OpsHandler) GetOpenAIWSV2PassthroughMetrics(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{
		"passthrough": openaiwsv2.SnapshotMetrics(),
		"timestamp":   time.Now().UTC(),
	})
}
