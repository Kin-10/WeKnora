package router

import (
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

func RegisterAnonymousBidCheckRoutes(r *gin.RouterGroup, h *handler.AnonymousBidCheckHandler, g *rbacGuards) {
	checks := g.apiKeyGroup(r.Group("/anonymous-bid-check"), apiKeyFullAccess())
	checks.POST("", g.Viewer(), h.Check)
}
