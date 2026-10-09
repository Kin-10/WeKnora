package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestAnonymousBidCheckRouteAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		role  types.TenantRole
		scope *types.TenantAPIKeyScope
		want  int
	}{
		{"viewer", types.TenantRoleViewer, nil, http.StatusBadRequest},
		{"full API key", types.TenantRoleOwner, &types.TenantAPIKeyScope{FullAccess: true}, http.StatusBadRequest},
		{"chat-only API key", types.TenantRoleOwner, &types.TenantAPIKeyScope{Capabilities: types.StringArray{string(types.APIKeyCapabilityChat)}}, http.StatusForbidden},
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
			RegisterAnonymousBidCheckRoutes(engine.Group("/api/v1"), handler.NewAnonymousBidCheckHandler(nil, nil), guards)
			guards.assertAPIKeyPoliciesMatchRoutes(engine)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/anonymous-bid-check", nil))
			if recorder.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}
