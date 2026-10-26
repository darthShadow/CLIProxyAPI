package api

import (
	"github.com/gin-gonic/gin"
)

// registerUsageArchiveRoutes uses the existing v8 key and availability checks.
// Static assets carry no data and share the panel's availability check.
func (s *Server) registerUsageArchiveRoutes(v8 *gin.RouterGroup) {
	v8.GET("/observability/usage/archive", s.mgmt.GetUsageArchive)
	v8.POST("/observability/usage/accounts", s.mgmt.PostUsageAccounts)
	s.engine.GET("/usage-archive.html", s.managementAvailabilityMiddleware(), s.mgmt.GetUsageArchivePage)
	s.engine.GET("/usage-archive.js", s.managementAvailabilityMiddleware(), s.mgmt.GetUsageArchiveScript)
}
