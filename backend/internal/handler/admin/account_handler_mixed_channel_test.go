package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupAccountMixedChannelRouter(adminSvc *stubAdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	accountHandler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/check-mixed-channel", accountHandler.CheckMixedChannel)
	router.POST("/api/v1/admin/accounts", accountHandler.Create)
	router.PUT("/api/v1/admin/accounts/:id", accountHandler.Update)
	return router
}

func TestAccountHandlerCheckMixedChannelNoRisk(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"platform":  "antigravity",
		"group_ids": []int64{27},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/check-mixed-channel", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, false, data["has_risk"])
	require.Equal(t, int64(0), adminSvc.lastMixedCheck.accountID)
	require.Equal(t, "antigravity", adminSvc.lastMixedCheck.platform)
	require.Equal(t, []int64{27}, adminSvc.lastMixedCheck.groupIDs)
}

func TestAccountHandlerCheckMixedChannelWithRisk(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.checkMixedErr = &service.MixedChannelError{
		GroupID:         27,
		GroupName:       "claude-max",
		CurrentPlatform: "Antigravity",
		OtherPlatform:   "Anthropic",
	}
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"platform":   "antigravity",
		"group_ids":  []int64{27},
		"account_id": 99,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/check-mixed-channel", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
	data, ok := resp["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, data["has_risk"])
	require.Equal(t, "mixed_channel_warning", data["error"])
	details, ok := data["details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(27), details["group_id"])
	require.Equal(t, "claude-max", details["group_name"])
	require.Equal(t, "Antigravity", details["current_platform"])
	require.Equal(t, "Anthropic", details["other_platform"])
	require.Equal(t, int64(99), adminSvc.lastMixedCheck.accountID)
}

func TestAccountHandlerCreateMixedChannelConflictSimplifiedResponse(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.createAccountErr = &service.MixedChannelError{
		GroupID:         27,
		GroupName:       "claude-max",
		CurrentPlatform: "Antigravity",
		OtherPlatform:   "Anthropic",
	}
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"name":        "ag-oauth-1",
		"platform":    "antigravity",
		"type":        "oauth",
		"credentials": map[string]any{"refresh_token": "rt"},
		"group_ids":   []int64{27},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "mixed_channel_warning", resp["error"])
	require.Contains(t, resp["message"], "mixed_channel_warning")
	_, hasDetails := resp["details"]
	_, hasRequireConfirmation := resp["require_confirmation"]
	require.False(t, hasDetails)
	require.False(t, hasRequireConfirmation)
}

func TestAccountHandlerCreateSuccess(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"name":        "anthropic-key-1",
		"platform":    "anthropic",
		"type":        "apikey",
		"credentials": map[string]any{"api_key": "sk-ant-test"},
		"group_ids":   []int64{2},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
}

func TestAccountHandlerCreatePassesConfirmMixedChannelRisk(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"name":                       "ag-oauth-confirmed",
		"platform":                   "antigravity",
		"type":                       "oauth",
		"credentials":                map[string]any{"refresh_token": "rt"},
		"group_ids":                  []int64{27},
		"confirm_mixed_channel_risk": true,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.createdAccounts, 1)
	require.True(t, adminSvc.createdAccounts[0].SkipMixedChannelCheck)
}

func TestAccountHandlerCreateInvalidRequest(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp["message"], "Invalid request")
}

func TestAccountHandlerCreateRejectsNegativeRateMultiplier(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"name":            "anthropic-key-2",
		"platform":        "anthropic",
		"type":            "apikey",
		"credentials":     map[string]any{"api_key": "sk-ant-test"},
		"rate_multiplier": -1,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp["message"], "rate_multiplier must be >= 0")
}

func TestAccountHandlerCreateGenericErrorWithRetryAfter(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.createAccountErr = infraerrors.TooManyRequests("RATE_LIMITED", "too many requests").
		WithMetadata(map[string]string{"retry_after": "7"})
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"name":        "anthropic-key-3",
		"platform":    "anthropic",
		"type":        "apikey",
		"credentials": map[string]any{"api_key": "sk-ant-test"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Equal(t, "7", rec.Header().Get("Retry-After"))
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(http.StatusTooManyRequests), resp["code"])
}

func TestAccountHandlerUpdateInvalidAccountID(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/not-int", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp["message"], "Invalid account ID")
}

func TestAccountHandlerUpdateInvalidRequest(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body := []byte(`{"status":123}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp["message"], "Invalid request")
}

func TestAccountHandlerUpdateRejectsNegativeRateMultiplier(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"rate_multiplier": -0.1,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp["message"], "rate_multiplier must be >= 0")
}

func TestAccountHandlerUpdateGenericError(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.updateAccountErr = errors.New("update failed")
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"name": "new-name",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(http.StatusInternalServerError), resp["code"])
}

func TestAccountHandlerUpdatePassesConfirmMixedChannelRisk(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"confirm_mixed_channel_risk": true,
		"group_ids":                  []int64{27},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.updatedAccounts, 1)
	require.Len(t, adminSvc.updatedAccountIDs, 1)
	require.Equal(t, int64(3), adminSvc.updatedAccountIDs[0])
	require.True(t, adminSvc.updatedAccounts[0].SkipMixedChannelCheck)
}

func TestAccountHandlerUpdateMixedChannelConflictSimplifiedResponse(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.updateAccountErr = &service.MixedChannelError{
		GroupID:         27,
		GroupName:       "claude-max",
		CurrentPlatform: "Antigravity",
		OtherPlatform:   "Anthropic",
	}
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"group_ids": []int64{27},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "mixed_channel_warning", resp["error"])
	require.Contains(t, resp["message"], "mixed_channel_warning")
	_, hasDetails := resp["details"]
	_, hasRequireConfirmation := resp["require_confirmation"]
	require.False(t, hasDetails)
	require.False(t, hasRequireConfirmation)
}

func TestAccountHandlerUpdateAcceptsErrorStatus(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"status": "error",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
}

func TestAccountHandlerUpdateAcceptsDisabledStatus(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"status": "disabled",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, float64(0), resp["code"])
}

func TestAccountHandlerUpdateRejectsUnknownStatus(t *testing.T) {
	adminSvc := newStubAdminService()
	router := setupAccountMixedChannelRouter(adminSvc)

	body, _ := json.Marshal(map[string]any{
		"status": "paused",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Contains(t, resp["message"], "Invalid request")
}
