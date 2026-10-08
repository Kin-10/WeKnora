package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterBidOpportunitiesRoutes exposes public tender searches through the
// authenticated platform API. Searching does not mutate workspace resources.
func RegisterBidOpportunitiesRoutes(r *gin.RouterGroup, h *handler.BidOpportunitiesHandler, g *rbacGuards) {
	opportunities := g.apiKeyGroup(r.Group("/bid-opportunities"), apiKeyFullAccess())
	opportunities.POST("/search", g.Viewer(), h.Search)
}
