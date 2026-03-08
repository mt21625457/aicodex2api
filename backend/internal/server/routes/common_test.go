package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type readinessReporterStub struct {
	ready bool
}

func (s readinessReporterStub) IsReady() bool {
	return s.ready
}

func TestRegisterCommonRoutesReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCommonRoutes(r, readinessReporterStub{ready: true})

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"status":"ready"}`, w.Body.String())
}

func TestRegisterCommonRoutesReadyStarting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCommonRoutes(r, readinessReporterStub{ready: false})

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.JSONEq(t, `{"status":"starting"}`, w.Body.String())
}

func TestRegisterCommonRoutesReadyWithoutReporter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCommonRoutes(r, nil)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"status":"ready"}`, w.Body.String())
}

func TestRegisterCommonRoutesHealthAlwaysOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCommonRoutes(r, readinessReporterStub{ready: false})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}

func TestRegisterCommonRoutesTelemetryBatchAlwaysOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCommonRoutes(r, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/event_logging/batch", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestRegisterCommonRoutesSetupStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterCommonRoutes(r, nil)

	req := httptest.NewRequest(http.MethodGet, "/setup/status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"needs_setup":false`)
	require.Contains(t, w.Body.String(), `"step":"completed"`)
}
