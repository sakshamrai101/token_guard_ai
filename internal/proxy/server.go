package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/saksham/token-guard-ai/internal/config"
)

type ReadinessChecker interface {
	Ready() bool
}

type alwaysReady struct{}

func (alwaysReady) Ready() bool { return true }

type Server struct {
	cfg       config.Config
	handler   http.Handler
	admin     http.Handler
	ready     ReadinessChecker
	logger    *slog.Logger
	mux       *http.ServeMux
}

func NewServer(cfg config.Config, handler http.Handler, admin http.Handler, ready ReadinessChecker, logger *slog.Logger) *Server {
	if ready == nil {
		ready = alwaysReady{}
	}
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		cfg:     cfg,
		handler: handler,
		admin:   admin,
		ready:   ready,
		logger:  logger,
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/readyz", s.handleReadyz)
	if s.admin != nil {
		s.mux.Handle("/admin/", s.admin)
	}
	s.mux.Handle("/", s.handler)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("proxy listening", "addr", s.cfg.ListenAddr, "mode", s.cfg.EnforcementMode)
	return http.ListenAndServe(s.cfg.ListenAddr, s)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !s.ready.Ready() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "not_ready"})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}
