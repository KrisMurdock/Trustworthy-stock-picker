package aris

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Handler accepts names from an operator-loaded catalog, never request paths or
// runtime overrides. One active job and no queue bound memory and execution load.
type Handler struct {
	app     *App
	ctx     context.Context
	cancel  context.CancelFunc
	catalog map[string]Bundle
	mu      sync.Mutex
	active  bool
	closed  bool
	wg      sync.WaitGroup
}

func NewHandler(ctx context.Context, a *App, catalog map[string]Bundle) (*Handler, error) {
	copyCatalog := map[string]Bundle{}
	ids := map[string]bool{}
	for name, b := range catalog {
		if !ValidID(name) || ids[b.Request.RunID] {
			return nil, errors.New("invalid or duplicate catalog entry")
		}
		if e := b.validate(a.build); e != nil {
			return nil, e
		}
		ids[b.Request.RunID] = true
		raw, e := json.Marshal(b)
		if e != nil {
			return nil, e
		}
		var frozen Bundle
		if e = Decode(raw, &frozen); e != nil {
			return nil, e
		}
		copyCatalog[name] = frozen
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Handler{app: a, ctx: ctx, cancel: cancel, catalog: copyCatalog}, nil
}
func (h *Handler) Close() { h.mu.Lock(); h.closed = true; h.cancel(); h.mu.Unlock(); h.wg.Wait() }
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func httpError(w http.ResponseWriter, status int) {
	writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	host := r.Host
	if hostname, _, e := net.SplitHostPort(host); e == nil {
		host = hostname
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		httpError(w, 403)
		return
	}
	if r.URL.RawQuery != "" {
		httpError(w, 400)
		return
	}
	if r.Method == "POST" {
		typ, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || typ != "application/json" {
			httpError(w, 415)
			return
		}
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			httpError(w, 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	}
	if r.URL.Path == "/healthz" {
		if r.Method != "GET" {
			httpError(w, 405)
			return
		}
		writeJSON(w, 200, map[string]bool{"alive": true})
		return
	}
	if r.URL.Path == "/v1/runs" {
		if r.Method != "POST" {
			httpError(w, 405)
			return
		}
		h.create(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/runs/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/v1/runs/") || len(parts) < 1 || len(parts) > 2 || !ValidID(parts[0]) {
		httpError(w, 404)
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == "GET" {
		s, e := h.app.Status(id)
		if e != nil {
			httpError(w, 404)
			return
		}
		writeJSON(w, 200, s)
		return
	}
	if len(parts) != 2 {
		httpError(w, 405)
		return
	}
	switch parts[1] {
	case "report":
		if r.Method != "GET" {
			httpError(w, 405)
			return
		}
		s, e := h.app.Status(id)
		if e != nil {
			httpError(w, 404)
			return
		}
		if !terminal(s.Status) {
			httpError(w, 409)
			return
		}
		dir, _ := h.app.dir(id)
		raw, e := Read(filepath.Join(dir, "report.json"), 8<<20)
		if e != nil {
			httpError(w, 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	case "resume", "cancel":
		if r.Method != "POST" {
			httpError(w, 405)
			return
		}
		var empty struct{}
		if e := decodeBody(r, &empty); e != nil {
			httpError(w, 400)
			return
		}
		s, e := h.app.Status(id)
		if e != nil {
			httpError(w, 404)
			return
		}
		if terminal(s.Status) {
			httpError(w, 409)
			return
		}
		if parts[1] == "cancel" {
			if e = h.app.Cancel(id); e != nil {
				httpError(w, 409)
				return
			}
			writeJSON(w, 202, map[string]string{"run_id": id})
			return
		}
		if e = h.app.Verify(id); e != nil {
			httpError(w, 409)
			return
		}
		if !h.launch(func() {
			_, e := h.app.Resume(h.ctx, id)
			if e != nil {
				h.app.log.Error("Research resume failed", "run_id", id)
			}
		}) {
			httpError(w, 429)
			return
		}
		writeJSON(w, 202, map[string]string{"run_id": id})
	default:
		httpError(w, 404)
	}
}
func decodeBody(r *http.Request, v any) error {
	raw, e := ioReadBody(r)
	if e != nil {
		return e
	}
	return Decode(raw, v)
}
func ioReadBody(r *http.Request) ([]byte, error) { return io.ReadAll(r.Body) }
func (h *Handler) launch(fn func()) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active || h.closed || h.ctx.Err() != nil {
		return false
	}
	h.active = true
	h.wg.Add(1)
	go func() { defer h.wg.Done(); defer func() { h.mu.Lock(); h.active = false; h.mu.Unlock() }(); fn() }()
	return true
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version int    `json:"schema_version"`
		Bundle  string `json:"bundle"`
	}
	if e := decodeBody(r, &req); e != nil || req.Version != 1 || !ValidID(req.Bundle) {
		httpError(w, 400)
		return
	}
	b, ok := h.catalog[req.Bundle]
	if !ok {
		httpError(w, 400)
		return
	}
	// Reservation and initial freeze happen before 202. The HTTP process cannot
	// acknowledge an in-memory-only job that would vanish on a crash.
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, e := h.app.Status(b.Request.RunID); e == nil {
		if s.BundleHash != Digest(b) {
			httpError(w, 409)
			return
		}
		writeJSON(w, 202, s)
		return
	} else if !os.IsNotExist(e) {
		httpError(w, 409)
		return
	}
	if h.active || h.closed || h.ctx.Err() != nil {
		httpError(w, 429)
		return
	}
	s, dir, unlock, e := h.app.freeze(h.ctx, b)
	if e != nil {
		httpError(w, 409)
		return
	}
	h.active = true
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer unlock()
		defer func() { h.mu.Lock(); h.active = false; h.mu.Unlock() }()
		if _, e := h.app.execute(h.ctx, dir, b, s); e != nil {
			h.app.log.Error("Research execution failed", "run_id", s.RunID)
		}
	}()
	writeJSON(w, 202, s)
}

func Serve(ctx context.Context, addr string, h *Handler) error {
	host, _, e := net.SplitHostPort(addr)
	if e != nil {
		return e
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("HTTP listen address must be an explicit loopback IP")
	}
	ln, e := net.Listen("tcp", addr)
	if e != nil {
		return e
	}
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		case <-stopped:
		}
	}()
	e = server.Serve(ln)
	h.Close()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
