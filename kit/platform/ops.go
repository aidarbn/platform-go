package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	healthTimeout     = 3 * time.Second
	readHeaderTimeout = 5 * time.Second
)

// opsServer — служебный сервер: /health и /metrics. Поднимается всегда,
// отдельно от прикладных портов, чтобы проверки не зависели от модуля api.
type opsServer struct {
	srv *http.Server
	ln  net.Listener
}

func startOps(addr string, app *App) (*opsServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", app.handleHealth)
	mux.Handle("GET /metrics", promhttp.HandlerFor(app.Metrics(), promhttp.HandlerOpts{}))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("служебный сервер на %s: %w", addr, err)
	}

	s := &opsServer{
		srv: &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout},
		ln:  ln,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.log.Error("служебный сервер остановлен с ошибкой", "err", err)
		}
	}()
	return s, nil
}

func (s *opsServer) addr() string { return s.ln.Addr().String() }

func (s *opsServer) stop(ctx context.Context) error { return s.srv.Shutdown(ctx) }

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()

	checks := a.healthChecks()
	errs := make([]error, len(checks))

	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.check(ctx)
		}()
	}
	wg.Wait()

	resp := healthResponse{Status: "ok", Checks: make(map[string]string, len(checks))}
	code := http.StatusOK
	for i, c := range checks {
		if errs[i] != nil {
			resp.Checks[c.name] = errs[i].Error()
			resp.Status = "fail"
			code = http.StatusServiceUnavailable
			continue
		}
		resp.Checks[c.name] = "ok"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		a.log.Error("не удалось отдать health", "err", err)
	}
}
