package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/ZanyK4502/DeBox-Asset-alert/internal/config"
)

type handler struct {
	cfg config.Config
}

func New(cfg config.Config) http.Handler {
	h := handler{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/ready", h.ready)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir(cfg.StaticDir))))
	mux.HandleFunc("GET /", h.index)
	return mux
}

func (h handler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(h.cfg.StaticDir, "index.html"))
}

func (h handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"app":          h.cfg.AppName,
		"environment":  h.cfg.Environment,
		"receive_mode": h.cfg.ReceiveMode,
	})
}

func (h handler) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": "ready",
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
