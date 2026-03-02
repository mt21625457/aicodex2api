package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type settingHandlerTemplateRepoStub struct {
	values map[string]string
}

func newSettingHandlerTemplateRepoStub() *settingHandlerTemplateRepoStub {
	return &settingHandlerTemplateRepoStub{values: map[string]string{}}
}

func (s *settingHandlerTemplateRepoStub) Get(ctx context.Context, key string) (*service.Setting, error) {
	value, err := s.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (s *settingHandlerTemplateRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (s *settingHandlerTemplateRepoStub) Set(ctx context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *settingHandlerTemplateRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (s *settingHandlerTemplateRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	for key, value := range settings {
		s.values[key] = value
	}
	return nil
}

func (s *settingHandlerTemplateRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	for key, value := range s.values {
		out[key] = value
	}
	return out, nil
}

func (s *settingHandlerTemplateRepoStub) Delete(ctx context.Context, key string) error {
	delete(s.values, key)
	return nil
}

type failingSettingRepoStub struct{}

func (s *failingSettingRepoStub) Get(ctx context.Context, key string) (*service.Setting, error) {
	return nil, errors.New("boom")
}
func (s *failingSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	return "", errors.New("boom")
}
func (s *failingSettingRepoStub) Set(ctx context.Context, key, value string) error {
	return errors.New("boom")
}
func (s *failingSettingRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	return nil, errors.New("boom")
}
func (s *failingSettingRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	return errors.New("boom")
}
func (s *failingSettingRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	return nil, errors.New("boom")
}
func (s *failingSettingRepoStub) Delete(ctx context.Context, key string) error {
	return errors.New("boom")
}

func setupBulkEditTemplateRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	repo := newSettingHandlerTemplateRepoStub()
	settingService := service.NewSettingService(repo, nil)
	handler := NewSettingHandler(settingService, nil, nil, nil, nil)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		uid := int64(1)
		if header := c.GetHeader("X-User-ID"); header != "" {
			if parsed, err := strconv.ParseInt(header, 10, 64); err == nil && parsed > 0 {
				uid = parsed
			}
		}
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: uid})
		c.Next()
	})

	router.GET("/api/v1/admin/settings/bulk-edit-templates", handler.ListBulkEditTemplates)
	router.POST("/api/v1/admin/settings/bulk-edit-templates", handler.UpsertBulkEditTemplate)
	router.DELETE("/api/v1/admin/settings/bulk-edit-templates/:template_id", handler.DeleteBulkEditTemplate)
	router.GET("/api/v1/admin/settings/bulk-edit-templates/:template_id/versions", handler.ListBulkEditTemplateVersions)
	router.POST("/api/v1/admin/settings/bulk-edit-templates/:template_id/rollback", handler.RollbackBulkEditTemplate)

	return router
}

func decodeResponseDataMap(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload response.Response
	require.NoError(t, json.Unmarshal(body, &payload))
	if payload.Data == nil {
		return map[string]any{}
	}
	asMap, ok := payload.Data.(map[string]any)
	require.True(t, ok)
	return asMap
}

func TestSettingHandlerBulkEditTemplate_CRUDFlow(t *testing.T) {
	router := setupBulkEditTemplateRouter()

	createBody := map[string]any{
		"name":           "OpenAI OAuth Baseline",
		"scope_platform": "openai",
		"scope_type":     "oauth",
		"share_scope":    "team",
		"state": map[string]any{
			"enableOpenAIPassthrough": true,
		},
	}
	raw, err := json.Marshal(createBody)
	require.NoError(t, err)

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/bulk-edit-templates", bytes.NewReader(raw))
	createReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code)

	createData := decodeResponseDataMap(t, createRec.Body.Bytes())
	templateID, ok := createData["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, templateID)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates?scope_platform=openai&scope_type=oauth",
		nil,
	)
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)

	listData := decodeResponseDataMap(t, listRec.Body.Bytes())
	items, ok := listData["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)

	deleteRec := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/admin/settings/bulk-edit-templates/"+templateID,
		nil,
	)
	router.ServeHTTP(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code)

	listAfterDeleteRec := httptest.NewRecorder()
	listAfterDeleteReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates?scope_platform=openai&scope_type=oauth",
		nil,
	)
	router.ServeHTTP(listAfterDeleteRec, listAfterDeleteReq)
	require.Equal(t, http.StatusOK, listAfterDeleteRec.Code)

	listAfterDeleteData := decodeResponseDataMap(t, listAfterDeleteRec.Body.Bytes())
	itemsAfterDelete, ok := listAfterDeleteData["items"].([]any)
	require.True(t, ok)
	require.Len(t, itemsAfterDelete, 0)
}

func TestSettingHandlerBulkEditTemplate_VersionsAndRollback(t *testing.T) {
	router := setupBulkEditTemplateRouter()

	createBody := map[string]any{
		"name":           "Rollback Target",
		"scope_platform": "openai",
		"scope_type":     "oauth",
		"share_scope":    "groups",
		"group_ids":      []int64{2},
		"state": map[string]any{
			"enableOpenAIWSMode": true,
		},
	}
	createRaw, err := json.Marshal(createBody)
	require.NoError(t, err)

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/settings/bulk-edit-templates",
		bytes.NewReader(createRaw),
	)
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-User-ID", "9")
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code)
	createData := decodeResponseDataMap(t, createRec.Body.Bytes())
	templateID := createData["id"].(string)

	updateBody := map[string]any{
		"id":             templateID,
		"name":           "Rollback Target",
		"scope_platform": "openai",
		"scope_type":     "oauth",
		"share_scope":    "team",
		"group_ids":      []int64{},
		"state": map[string]any{
			"enableOpenAIWSMode": false,
		},
	}
	updateRaw, err := json.Marshal(updateBody)
	require.NoError(t, err)

	updateRec := httptest.NewRecorder()
	updateReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/settings/bulk-edit-templates",
		bytes.NewReader(updateRaw),
	)
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("X-User-ID", "9")
	router.ServeHTTP(updateRec, updateReq)
	require.Equal(t, http.StatusOK, updateRec.Code)

	versionsRec := httptest.NewRecorder()
	versionsReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates/"+templateID+"/versions?scope_group_ids=2",
		nil,
	)
	versionsReq.Header.Set("X-User-ID", "9")
	router.ServeHTTP(versionsRec, versionsReq)
	require.Equal(t, http.StatusOK, versionsRec.Code)
	versionsData := decodeResponseDataMap(t, versionsRec.Body.Bytes())
	versions := versionsData["items"].([]any)
	require.Len(t, versions, 1)
	versionID := versions[0].(map[string]any)["version_id"].(string)

	rollbackBody := map[string]any{"version_id": versionID}
	rollbackRaw, err := json.Marshal(rollbackBody)
	require.NoError(t, err)

	rollbackRec := httptest.NewRecorder()
	rollbackReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/settings/bulk-edit-templates/"+templateID+"/rollback?scope_group_ids=2",
		bytes.NewReader(rollbackRaw),
	)
	rollbackReq.Header.Set("Content-Type", "application/json")
	rollbackReq.Header.Set("X-User-ID", "9")
	router.ServeHTTP(rollbackRec, rollbackReq)
	require.Equal(t, http.StatusOK, rollbackRec.Code)
	rollbackData := decodeResponseDataMap(t, rollbackRec.Body.Bytes())
	require.Equal(t, "groups", rollbackData["share_scope"])
	groupIDs := rollbackData["group_ids"].([]any)
	require.Equal(t, []any{float64(2)}, groupIDs)
	state := rollbackData["state"].(map[string]any)
	require.Equal(t, true, state["enableOpenAIWSMode"])

	versionsAfterRollbackRec := httptest.NewRecorder()
	versionsAfterRollbackReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates/"+templateID+"/versions?scope_group_ids=2",
		nil,
	)
	versionsAfterRollbackReq.Header.Set("X-User-ID", "9")
	router.ServeHTTP(versionsAfterRollbackRec, versionsAfterRollbackReq)
	require.Equal(t, http.StatusOK, versionsAfterRollbackRec.Code)
	versionsAfterRollbackData := decodeResponseDataMap(t, versionsAfterRollbackRec.Body.Bytes())
	versionsAfterRollback := versionsAfterRollbackData["items"].([]any)
	require.Len(t, versionsAfterRollback, 2)
}

func TestSettingHandlerBulkEditTemplate_Validation(t *testing.T) {
	router := setupBulkEditTemplateRouter()

	invalidCreateBody := map[string]any{
		"name":           "Groups Template",
		"scope_platform": "openai",
		"scope_type":     "oauth",
		"share_scope":    "groups",
		"group_ids":      []int64{},
		"state":          map[string]any{},
	}
	raw, err := json.Marshal(invalidCreateBody)
	require.NoError(t, err)

	invalidCreateRec := httptest.NewRecorder()
	invalidCreateReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/bulk-edit-templates", bytes.NewReader(raw))
	invalidCreateReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(invalidCreateRec, invalidCreateReq)
	require.Equal(t, http.StatusBadRequest, invalidCreateRec.Code)

	invalidListRec := httptest.NewRecorder()
	invalidListReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates?scope_group_ids=abc",
		nil,
	)
	router.ServeHTTP(invalidListRec, invalidListReq)
	require.Equal(t, http.StatusBadRequest, invalidListRec.Code)
}

func TestSettingHandlerBulkEditTemplate_PrivateVisibilityAndDeletePermission(t *testing.T) {
	router := setupBulkEditTemplateRouter()

	createBody := map[string]any{
		"name":           "Private Template",
		"scope_platform": "openai",
		"scope_type":     "oauth",
		"share_scope":    "private",
		"state": map[string]any{
			"enableBaseUrl": true,
		},
	}
	raw, err := json.Marshal(createBody)
	require.NoError(t, err)

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/bulk-edit-templates", bytes.NewReader(raw))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-User-ID", "100")
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code)

	createData := decodeResponseDataMap(t, createRec.Body.Bytes())
	templateID := createData["id"].(string)

	listByOtherRec := httptest.NewRecorder()
	listByOtherReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates?scope_platform=openai&scope_type=oauth",
		nil,
	)
	listByOtherReq.Header.Set("X-User-ID", "200")
	router.ServeHTTP(listByOtherRec, listByOtherReq)
	require.Equal(t, http.StatusOK, listByOtherRec.Code)

	listByOtherData := decodeResponseDataMap(t, listByOtherRec.Body.Bytes())
	items, ok := listByOtherData["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 0)

	deleteByOtherRec := httptest.NewRecorder()
	deleteByOtherReq := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/admin/settings/bulk-edit-templates/"+templateID,
		nil,
	)
	deleteByOtherReq.Header.Set("X-User-ID", "200")
	router.ServeHTTP(deleteByOtherRec, deleteByOtherReq)
	require.Equal(t, http.StatusForbidden, deleteByOtherRec.Code)

	deleteByOwnerRec := httptest.NewRecorder()
	deleteByOwnerReq := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/admin/settings/bulk-edit-templates/"+templateID,
		nil,
	)
	deleteByOwnerReq.Header.Set("X-User-ID", "100")
	router.ServeHTTP(deleteByOwnerRec, deleteByOwnerReq)
	require.Equal(t, http.StatusOK, deleteByOwnerRec.Code)
}

func TestSettingHandlerBulkEditTemplate_GroupsVisibilityByScopeGroupIDs(t *testing.T) {
	router := setupBulkEditTemplateRouter()

	createBody := map[string]any{
		"name":           "Group Shared",
		"scope_platform": "openai",
		"scope_type":     "oauth",
		"share_scope":    "groups",
		"group_ids":      []int64{3, 8},
		"state":          map[string]any{"enableOpenAIWSMode": true},
	}
	raw, err := json.Marshal(createBody)
	require.NoError(t, err)

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/bulk-edit-templates", bytes.NewReader(raw))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-User-ID", "1")
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code)

	invisibleRec := httptest.NewRecorder()
	invisibleReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates?scope_platform=openai&scope_type=oauth&scope_group_ids=9",
		nil,
	)
	invisibleReq.Header.Set("X-User-ID", "2")
	router.ServeHTTP(invisibleRec, invisibleReq)
	require.Equal(t, http.StatusOK, invisibleRec.Code)
	invisibleData := decodeResponseDataMap(t, invisibleRec.Body.Bytes())
	invisibleItems := invisibleData["items"].([]any)
	require.Len(t, invisibleItems, 0)

	visibleRec := httptest.NewRecorder()
	visibleReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates?scope_platform=openai&scope_type=oauth&scope_group_ids=8",
		nil,
	)
	visibleReq.Header.Set("X-User-ID", "2")
	router.ServeHTTP(visibleRec, visibleReq)
	require.Equal(t, http.StatusOK, visibleRec.Code)
	visibleData := decodeResponseDataMap(t, visibleRec.Body.Bytes())
	visibleItems := visibleData["items"].([]any)
	require.Len(t, visibleItems, 1)
}

func TestSettingHandlerBulkEditTemplate_UnauthorizedAndInvalidRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newSettingHandlerTemplateRepoStub()
	settingService := service.NewSettingService(repo, nil)
	handler := NewSettingHandler(settingService, nil, nil, nil, nil)

	router := gin.New()
	router.GET("/list", handler.ListBulkEditTemplates)
	router.GET("/versions/:template_id", handler.ListBulkEditTemplateVersions)
	router.POST("/rollback/:template_id", handler.RollbackBulkEditTemplate)
	router.POST("/upsert", handler.UpsertBulkEditTemplate)
	router.DELETE("/delete/:template_id", handler.DeleteBulkEditTemplate)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/list", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/upsert", bytes.NewBufferString("{bad-json"))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/delete/%20", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/versions/abc", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/rollback/abc", bytes.NewBufferString(`{"version_id":"v1"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestParseScopeGroupIDs(t *testing.T) {
	ids, err := parseScopeGroupIDs("")
	require.NoError(t, err)
	require.Nil(t, ids)

	ids, err = parseScopeGroupIDs("1, 2,2,3")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3}, ids)

	_, err = parseScopeGroupIDs("x,2")
	require.Error(t, err)
}

func TestSettingHandlerBulkEditTemplate_BindErrorAndMissingTemplateID(t *testing.T) {
	router := setupBulkEditTemplateRouter()

	bindErrRec := httptest.NewRecorder()
	bindErrReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/settings/bulk-edit-templates",
		bytes.NewBufferString("{bad-json"),
	)
	bindErrReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(bindErrRec, bindErrReq)
	require.Equal(t, http.StatusBadRequest, bindErrRec.Code)

	missingIDRec := httptest.NewRecorder()
	missingIDReq := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/admin/settings/bulk-edit-templates/%20",
		nil,
	)
	router.ServeHTTP(missingIDRec, missingIDReq)
	require.Equal(t, http.StatusBadRequest, missingIDRec.Code)

	invalidScopeRec := httptest.NewRecorder()
	invalidScopeReq := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/settings/bulk-edit-templates/abc/versions?scope_group_ids=bad",
		nil,
	)
	router.ServeHTTP(invalidScopeRec, invalidScopeReq)
	require.Equal(t, http.StatusBadRequest, invalidScopeRec.Code)

	rollbackMissingIDRec := httptest.NewRecorder()
	rollbackMissingIDReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/settings/bulk-edit-templates/%20/rollback",
		bytes.NewBufferString(`{"version_id":"v1"}`),
	)
	rollbackMissingIDReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rollbackMissingIDRec, rollbackMissingIDReq)
	require.Equal(t, http.StatusBadRequest, rollbackMissingIDRec.Code)

	rollbackInvalidScopeRec := httptest.NewRecorder()
	rollbackInvalidScopeReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/settings/bulk-edit-templates/abc/rollback?scope_group_ids=bad",
		bytes.NewBufferString(`{"version_id":"v1"}`),
	)
	rollbackInvalidScopeReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rollbackInvalidScopeRec, rollbackInvalidScopeReq)
	require.Equal(t, http.StatusBadRequest, rollbackInvalidScopeRec.Code)

	rollbackBindErrRec := httptest.NewRecorder()
	rollbackBindErrReq := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/admin/settings/bulk-edit-templates/abc/rollback",
		bytes.NewBufferString("{bad-json"),
	)
	rollbackBindErrReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rollbackBindErrRec, rollbackBindErrReq)
	require.Equal(t, http.StatusBadRequest, rollbackBindErrRec.Code)
}

func TestSettingHandlerBulkEditTemplate_ListErrorFromService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settingService := service.NewSettingService(&failingSettingRepoStub{}, nil)
	handler := NewSettingHandler(settingService, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Next()
	})
	router.GET("/list", handler.ListBulkEditTemplates)
	router.GET("/versions/:template_id", handler.ListBulkEditTemplateVersions)
	router.POST("/rollback/:template_id", handler.RollbackBulkEditTemplate)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/list", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/versions/tpl-1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/rollback/tpl-1", bytes.NewBufferString(`{"version_id":"v1"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}
