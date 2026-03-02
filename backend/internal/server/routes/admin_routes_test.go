package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterAdminRoutes_RegistersAPIKeyGroupUpdateRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	handlers := &handler.Handlers{
		Admin: &handler.AdminHandlers{},
	}
	adminAuth := middleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() })

	require.NotPanics(t, func() {
		RegisterAdminRoutes(v1, handlers, adminAuth)
	})
	require.True(t, hasRoute(router, "PUT", "/api/v1/admin/api-keys/:id"))
}

func hasRoute(router *gin.Engine, method, path string) bool {
	for _, route := range router.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
