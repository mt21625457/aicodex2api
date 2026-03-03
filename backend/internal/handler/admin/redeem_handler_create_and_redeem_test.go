package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupCreateAndRedeemRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewRedeemHandler(newStubAdminService(), &service.RedeemService{})
	router.POST("/api/v1/admin/redeem-codes/create-and-redeem", h.CreateAndRedeem)
	return router
}

func TestCreateAndRedeem_RejectsUnsupportedType(t *testing.T) {
	router := setupCreateAndRedeemRouter()
	body := `{"code":"ORDER-123","type":"subscription","value":100,"user_id":1}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/create-and-redeem", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid request")
}

func TestCreateAndRedeem_RejectsTrimmedEmptyCode(t *testing.T) {
	router := setupCreateAndRedeemRouter()
	body := `{"code":"   ","type":"balance","value":100,"user_id":1}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/redeem-codes/create-and-redeem", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "code length must be between 3 and 128")
}
