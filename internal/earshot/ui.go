package earshot

import (
	"embed"
	"html/template"
	"net/http"
)

//go:embed ui/*
var uiFiles embed.FS

var uiPage = template.Must(template.ParseFS(uiFiles, "ui/index.html"))

func registerUI(mux *http.ServeMux, config Config) {
	mux.HandleFunc("GET /ui", func(w http.ResponseWriter, _ *http.Request) {
		// Preserve any prefix added by a reverse proxy.
		w.Header().Set("Location", "ui/")
		w.WriteHeader(http.StatusPermanentRedirect)
	})
	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "microphone=(self)")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; media-src blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		_ = uiPage.Execute(w, struct {
			RequireToken bool
			MaxBytes     int64
			MaxSeconds   int
		}{!config.NoAuth, config.MaxBytes, config.MaxSeconds})
	})
	for _, asset := range []string{"style.css", "app.js"} {
		mux.HandleFunc("GET /ui/"+asset, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, uiFiles, "ui/"+asset)
		})
	}
}
