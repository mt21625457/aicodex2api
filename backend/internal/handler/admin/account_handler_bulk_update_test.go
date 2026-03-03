package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupAccountBulkUpdateRouter(adminSvc *stubAdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accountHandler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/bulk-update", accountHandler.BulkUpdate)
	return router
}

func TestAccountHandlerBulkUpdate_ForwardsAutoPauseOnExpired(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountBulkUpdateRouter(adminSvc)

	body, err := json.Marshal(map[string]any{
		"account_ids":           []int64{1},
		"auto_pause_on_expired": true,
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/bulk-update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, adminSvc.lastBulkUpdateInput)
	require.NotNil(t, adminSvc.lastBulkUpdateInput.AutoPauseOnExpired)
	require.True(t, *adminSvc.lastBulkUpdateInput.AutoPauseOnExpired)
}

func TestAccountHandlerBulkUpdate_RejectsEmptyUpdates(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountBulkUpdateRouter(adminSvc)

	body, err := json.Marshal(map[string]any{
		"account_ids": []int64{1},
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/bulk-update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp["message"], "No updates provided")
}
