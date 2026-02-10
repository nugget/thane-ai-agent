// Package web provides the chat web interface for Thane.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

// Handler returns an http.Handler that serves the chat UI.
// Mount this at "/chat" or "/" as desired.
func Handler() http.Handler {
	// Strip the "static" prefix from embedded files
	subFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}

	fileServer := http.FileServer(http.FS(subFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve index.html for the root path
		if r.URL.Path == "/" || r.URL.Path == "" {
			r.URL.Path = "/index.html"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// RegisterRoutes adds the chat UI routes to a mux.
// It registers both /chat and /chat/* paths.
func RegisterRoutes(mux *http.ServeMux) {
	handler := Handler()

	// Serve chat UI at /chat
	mux.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/"
		handler.ServeHTTP(w, r)
	})

	// Serve chat UI assets at /chat/*
	mux.HandleFunc("/chat/", func(w http.ResponseWriter, r *http.Request) {
		// Strip /chat prefix for file serving
		r.URL.Path = r.URL.Path[5:] // len("/chat") = 5
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		handler.ServeHTTP(w, r)
	})

	// Also serve manifest at root for PWA
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/manifest.json"
		handler.ServeHTTP(w, r)
	})
}
