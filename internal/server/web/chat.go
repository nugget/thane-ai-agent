package web

import "net/http"

// ChatData is the template context for the chat page.
type ChatData struct {
	PageData
}

// handleChat renders the chat page wrapped in the shared layout.
func (s *WebServer) handleChat(w http.ResponseWriter, r *http.Request) {
	data := ChatData{
		PageData: PageData{
			BrandName: s.brandName,
			ActiveNav: "chat",
		},
	}
	s.render(w, r, "chat.html", data)
}
