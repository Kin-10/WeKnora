package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// Invalid search input exercises the real route guards without contacting the
// external provider: an authorized caller reaches validation and receives 400.
func TestBidOpportunitiesRouteAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		role  types.TenantRole
		scope *types.TenantAPIKeyScope
		want  int
	}{
		{name: "viewer", role: types.TenantRoleViewer, want: http.StatusBadRequest},
		{name: "full API key", role: types.TenantRoleOwner, scope: &types.TenantAPIKeyScope{FullAccess: true}, want: http.StatusBadRequest},
		{name: "chat-only API key", role: types.TenantRoleOwner, scope: &types.TenantAPIKeyScope{Capabilities: types.StringArray{string(types.APIKeyCapabilityChat)}}, want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enabled := true
			guards := &rbacGuards{cfg: &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}}
			engine := gin.New()
			engine.Use(middleware.ErrorHandler())
			engine.Use(func(c *gin.Context) {
				ctx := context.WithValue(c.Request.Context(), types.TenantRoleContextKey, tc.role)
				if tc.scope != nil {
					ctx = types.WithTenantAPIKeyScope(ctx, *tc.scope)
				}
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			})
			engine.Use(guards.ensureAPIKeyAuthorizer().Middleware())
			RegisterBidOpportunitiesRoutes(engine.Group("/api/v1"), handler.NewBidOpportunitiesHandler(), guards)
			guards.assertAPIKeyPoliciesMatchRoutes(engine)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/bid-opportunities/search", strings.NewReader(`{"pageNum":101}`))
			req.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}
