package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterGatewayRoutes_RegistersOpenAICompactRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxBodySize: 1024,
		},
	}

	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: &handler.OpenAIGatewayHandler{},
			SoraGateway:   &handler.SoraGatewayHandler{},
		},
		middleware.APIKeyAuthMiddleware(func(c *gin.Context) { c.Next() }),
		nil,
		nil,
		nil,
		nil,
		cfg,
	)

	routes := router.Routes()
	requireRoute := func(method, path string) {
		t.Helper()
		for _, route := range routes {
			if route.Method == method && route.Path == path {
				return
			}
		}
		require.Failf(t, "route not found", "method=%s path=%s", method, path)
	}

	requireRoute("POST", "/v1/responses/*subpath")
	requireRoute("POST", "/responses/*subpath")
}
