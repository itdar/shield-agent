package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

// StaticHandler returns an http.Handler that serves the embedded SPA.
// All requests to /ui/* are served from the embedded static/ directory.
func StaticHandler() http.Handler {
	sub, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(sub))

	return http.StripPrefix("/ui/", fileServer)
}

// RegisterUI registers the web UI routes (static + API) on the given mux.
func RegisterUI(mux *http.ServeMux, api *API) {
	// Static files at /ui/
	mux.Handle("/ui/", StaticHandler())
	// Redirect /ui to /ui/
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	// API endpoints
	api.RegisterRoutes(mux)
}
