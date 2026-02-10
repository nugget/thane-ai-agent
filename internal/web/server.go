// Package web provides the chat web interface for Thane.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

// getStaticFS returns the embedded static filesystem.
func getStaticFS() fs.FS {
	subFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return subFS
}

// serveFile serves a specific file from the embedded filesystem.
func serveFile(w http.ResponseWriter, r *http.Request, name string) {
	staticFS := getStaticFS()
	content, err := fs.ReadFile(staticFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Set content type based on file extension
	switch {
	case len(name) > 5 && name[len(name)-5:] == ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case len(name) > 5 && name[len(name)-5:] == ".json":
		w.Header().Set("Content-Type", "application/json")
	case len(name) > 4 && name[len(name)-4:] == ".css":
		w.Header().Set("Content-Type", "text/css")
	case len(name) > 3 && name[len(name)-3:] == ".js":
		w.Header().Set("Content-Type", "application/javascript")
	}

	w.Write(content)
}

// RegisterRoutes adds the chat UI routes to a mux.
func RegisterRoutes(mux *http.ServeMux) {
	// Serve chat UI at /chat
	mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, "index.html")
	})

	// Serve chat UI assets at /chat/* (for any future assets)
	mux.HandleFunc("GET /chat/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		if path == "" {
			serveFile(w, r, "index.html")
			return
		}
		serveFile(w, r, path)
	})

	// Serve manifest at root for PWA
	mux.HandleFunc("GET /manifest.json", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, "manifest.json")
	})
}
