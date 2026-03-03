package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newOpsOpenAIWSV2TestRouter(handler *OpsHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/metrics", handler.GetOpenAIWSV2PassthroughMetrics)
	return r
}

func TestOpsOpenAIWSV2Handler_GetPassthroughMetrics_ServiceUnavailable(t *testing.T) {
	r := newOpsOpenAIWSV2TestRouter(NewOpsHandler(nil))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestOpsOpenAIWSV2Handler_GetPassthroughMetrics_Success(t *testing.T) {
	r := newOpsOpenAIWSV2TestRouter(NewOpsHandler(newRuntimeOpsService(t)))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if code, _ := payload["code"].(float64); int(code) != 0 {
		t.Fatalf("code=%v, want 0", payload["code"])
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data field: %v", payload)
	}
	passthrough, ok := data["passthrough"].(map[string]any)
	if !ok {
		t.Fatalf("missing passthrough field: %v", data)
	}
	if _, ok := passthrough["semantic_mutation_total"].(float64); !ok {
		t.Fatalf("missing semantic_mutation_total: %v", passthrough)
	}
	if _, ok := passthrough["usage_parse_failure_total"].(float64); !ok {
		t.Fatalf("missing usage_parse_failure_total: %v", passthrough)
	}
	if _, ok := data["timestamp"].(string); !ok {
		t.Fatalf("missing timestamp: %v", data)
	}
}
