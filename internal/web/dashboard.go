package web

import (
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
)

// DashboardData is the template context for the runtime overview page.
type DashboardData struct {
	ActiveNav string
	Stats     StatsSnapshot
	Router    RouterInfo
	Health    map[string]HealthStatus
	Uptime    time.Duration
}

// handleDashboard renders the runtime overview page at "/". Only exact
// "/" requests get the dashboard; all other paths return 404.
func (s *WebServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := DashboardData{
		ActiveNav: "overview",
		Uptime:    buildinfo.Uptime(),
	}

	if s.statsFunc != nil {
		data.Stats = s.statsFunc()
	}
	if s.routerFunc != nil {
		data.Router = s.routerFunc()
	}
	if s.healthFunc != nil {
		data.Health = s.healthFunc()
	}

	s.render(w, r, "dashboard.html", data)
}
