// Package dashboard serves the embedded read-only stock dashboard.
package dashboard

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/health"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/selection"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

//go:embed static
var assets embed.FS

const requestTimeout = 5 * time.Second

type snapshotReader interface {
	LoadDashboardHistory(context.Context, config.Stock, string) (market.History, error)
	LoadDashboard(context.Context, []config.Stock, string) (storage.DashboardSnapshot, error)
	LoadDashboardReport(context.Context, string) (selection.Report, error)
	LoadMinutes(context.Context, config.Stock, int) (storage.MinuteSnapshot, error)
}

type experimentReader interface {
	List(context.Context) ([]experiment.Record, error)
	Get(context.Context, string) (experiment.Record, error)
	History(context.Context, string, string, string) (market.History, error)
}

type handler struct {
	health      func(context.Context) (health.Status, error)
	experiments experimentReader
	stocks      []config.Stock
	adjust      string
	db          snapshotReader
	log         *logging.Logger
}

// NewHandler exposes only public market snapshots. It never starts synchronization
// or delivery work. The caller controls the listening address and server lifetime.
func NewHandler(c config.Config, db *storage.MarketDB, log *logging.Logger) http.Handler {
	return newHandler(c, db, log)
}

func newHandler(c config.Config, db snapshotReader, log *logging.Logger) http.Handler {
	if log == nil {
		log = logging.Nop()
	}
	h := &handler{experiments: storage.ExperimentView{Root: c.DataDir, RequireCIFS: c.RequireCIFS}, stocks: append([]config.Stock(nil), c.Stocks...), adjust: c.Daily.Adjust, db: db, log: log}
	if marketDB, ok := db.(*storage.MarketDB); ok {
		h.health = func(ctx context.Context) (health.Status, error) { return health.Read(ctx, c, marketDB, time.Now()) }
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch r.URL.Path {
	case "/healthz":
		writeJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	case "/readyz", "/api/status":
		if r.URL.RawQuery != "" {
			writeError(w, r, http.StatusBadRequest, "status does not accept query parameters")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if h.health == nil {
			writeError(w, r, http.StatusServiceUnavailable, "status unavailable")
			return
		}
		status, err := h.health(ctx)
		if err != nil {
			h.log.Warn("Runtime status unavailable", "error", err)
			if r.URL.Path == "/api/status" {
				status.Version = 1
				status.Ready = false
				status.Business = "degraded"
				status.Issues = append(status.Issues, "dependencies_unavailable")
				writeJSON(w, r, http.StatusOK, status)
			} else {
				writeError(w, r, http.StatusServiceUnavailable, "storage or calendar unavailable")
			}
			return
		}
		writeJSON(w, r, http.StatusOK, status)
	case "/api/dashboard":
		if r.URL.RawQuery != "" {
			writeError(w, r, http.StatusBadRequest, "dashboard does not accept query parameters")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		snapshot, err := h.db.LoadDashboard(ctx, h.stocks, h.adjust)
		if err != nil {
			h.readError(w, r, ctx, err)
			return
		}
		writeJSON(w, r, http.StatusOK, explainSnapshot(snapshot))
	case "/api/experiments", "/api/experiment-history":
		h.serveExperiments(w, r)
	case "/api/history":
		values, err := url.ParseQuery(r.URL.RawQuery)
		codes := values["code"]
		if err != nil || len(codes) != 1 || codes[0] == "" {
			writeError(w, r, http.StatusBadRequest, "one stock code is required")
			return
		}
		adjust := h.adjust
		if choices, present := values["adjust"]; present {
			if len(choices) != 1 || choices[0] == "" || (choices[0] != h.adjust && choices[0] != "none") {
				writeError(w, r, http.StatusBadRequest, "adjust must be the configured price basis or none")
				return
			}
			adjust = choices[0]
		}
		for key := range values {
			if key != "code" && key != "adjust" {
				writeError(w, r, http.StatusBadRequest, "unsupported history parameter")
				return
			}
		}
		var stock config.Stock
		for _, candidate := range h.stocks {
			if candidate.Code == codes[0] {
				stock = candidate
				break
			}
		}
		if stock.Code == "" {
			writeError(w, r, http.StatusNotFound, "stock not in pool")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		history, err := h.db.LoadDashboardHistory(ctx, stock, adjust)
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, r, http.StatusNotFound, "history not found")
			return
		}
		if err != nil {
			h.readError(w, r, ctx, err)
			return
		}
		writeJSON(w, r, http.StatusOK, history)
	case "/api/intraday":
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values) != 2 || len(values["code"]) != 1 || len(values["period"]) != 1 {
			writeError(w, r, http.StatusBadRequest, "one code and period are required")
			return
		}
		period, err := strconv.Atoi(values.Get("period"))
		if err != nil || !market.MinutePeriod(period) || strconv.Itoa(period) != values.Get("period") {
			writeError(w, r, http.StatusBadRequest, "period must be 5, 15, 30 or 60")
			return
		}
		var stock config.Stock
		for _, candidate := range h.stocks {
			if candidate.Code == values.Get("code") {
				stock = candidate
				break
			}
		}
		if stock.Code == "" {
			writeError(w, r, http.StatusNotFound, "stock not in pool")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		history, err := h.db.LoadMinutes(ctx, stock, period)
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, r, http.StatusNotFound, "minute history not found")
			return
		}
		if err != nil {
			h.readError(w, r, ctx, err)
			return
		}
		writeJSON(w, r, http.StatusOK, history)
	case "/api/reports":
		query, err := parseReportDate(r)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "as_of must be one date in YYYY-MM-DD format")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		report, err := h.db.LoadDashboardReport(ctx, query)
		if err != nil {
			h.readError(w, r, ctx, err)
			return
		}
		writeJSON(w, r, http.StatusOK, explainReport(report))
	case "/":
		h.asset(w, r, "index.html", "text/html; charset=utf-8")
	case "/stocks":
		h.asset(w, r, "stocks.html", "text/html; charset=utf-8")
	case "/chart":
		h.asset(w, r, "chart.html", "text/html; charset=utf-8")
	case "/rankings":
		h.asset(w, r, "rankings.html", "text/html; charset=utf-8")
	case "/experiments":
		h.asset(w, r, "experiments.html", "text/html; charset=utf-8")
	case "/assets/experiments.js":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "experiments.js", "text/javascript; charset=utf-8")
	case "/assets/experiments.css":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "experiments.css", "text/css; charset=utf-8")
	case "/calculator":
		h.asset(w, r, "calculator.html", "text/html; charset=utf-8")
	case "/assets/calculator.js", "/assets/strategy-model.js":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, r.URL.Path[len("/assets/"):], "text/javascript; charset=utf-8")
	case "/assets/calculator.css":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "calculator.css", "text/css; charset=utf-8")
	case "/assets/chart.js":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "chart.js", "text/javascript; charset=utf-8")
	case "/assets/common.js", "/assets/overview.js", "/assets/stocks.js", "/assets/chart-page.js", "/assets/rankings.js":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, r.URL.Path[len("/assets/"):], "text/javascript; charset=utf-8")
	case "/assets/style.css":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "style.css", "text/css; charset=utf-8")
	default:
		writeError(w, r, http.StatusNotFound, "not found")
	}
}

func parseReportDate(r *http.Request) (string, error) {
	values, queryErr := url.ParseQuery(r.URL.RawQuery)
	dates := values["as_of"]
	if queryErr != nil || len(values) != 1 || len(dates) != 1 || len(dates[0]) != len("2006-01-02") {
		return "", errors.New("invalid report date")
	}
	date, err := time.Parse("2006-01-02", dates[0])
	if err != nil || date.Format("2006-01-02") != dates[0] {
		return "", errors.New("invalid report date")
	}
	return dates[0], nil
}

func (h *handler) asset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	data, err := assets.ReadFile("static/" + name)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

func (h *handler) readError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	status, message := http.StatusServiceUnavailable, "market data is temporarily unavailable"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		status, message = http.StatusGatewayTimeout, "market data request timed out"
	} else if errors.Is(err, fs.ErrNotExist) {
		status, message = http.StatusNotFound, "report not found"
	}
	if status != http.StatusNotFound {
		h.log.Warn("Dashboard data request failed", "status", status)
	}
	writeError(w, r, status, message)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	writeJSON(w, r, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status, data = http.StatusServiceUnavailable, []byte(`{"error":"market data is temporarily unavailable"}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(append(data, '\n'))
	}
}
