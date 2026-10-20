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
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketcontext"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/review"
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
const reviewRequestTimeout = 15 * time.Second
const contextRequestTimeout = 20 * time.Second

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

type reviewReader interface {
	Day(context.Context, string, string) (review.Day, error)
	Event(context.Context, string, string, string) (review.Event, error)
}

type handler struct {
	health      func(context.Context) (health.Status, error)
	experiments experimentReader
	review      reviewReader
	config      config.Config
	llm         *config.LLMRuntime
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
	h := &handler{experiments: storage.ExperimentView{Root: c.DataDir, RequireCIFS: c.RequireCIFS}, config: c, llm: config.NewLLMRuntime(c.LLM), stocks: append([]config.Stock(nil), c.Stocks...), adjust: c.Daily.Adjust, db: db, log: log}
	if marketDB, ok := db.(*storage.MarketDB); ok {
		h.health = func(ctx context.Context) (health.Status, error) { return health.Read(ctx, c, marketDB, time.Now()) }
		h.review = review.New(newReviewLoader(c, marketDB))
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !allowedMethod(r) {
		w.Header().Set("Allow", allowedMethods(r.URL.Path))
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
	case "/api/market-context":
		h.serveMarketContext(w, r)
	case "/api/review":
		h.serveReviewDay(w, r)
	case "/api/review/event":
		h.serveReviewEvent(w, r)
	case "/api/master/roles":
		h.serveMasterRoles(w, r)
	case "/api/master/chat":
		h.serveMasterChat(w, r)
	case "/api/review/analysis":
		h.serveReviewAnalysis(w, r)
	case "/api/llm/config":
		h.serveLLMConfig(w, r)
	case "/api/llm/config/test":
		h.serveLLMConfigTest(w, r)
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
	case "/review":
		h.asset(w, r, "review.html", "text/html; charset=utf-8")
	case "/assets/experiments.js":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "experiments.js", "text/javascript; charset=utf-8")
	case "/assets/experiments.css":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "experiments.css", "text/css; charset=utf-8")
	case "/assets/review.js":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "review.js", "text/javascript; charset=utf-8")
	case "/assets/market-context.js":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "market-context.js", "text/javascript; charset=utf-8")
	case "/assets/review.css":
		w.Header().Set("Cache-Control", "no-cache")
		h.asset(w, r, "review.css", "text/css; charset=utf-8")
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

func allowedMethod(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/llm/config":
		return r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodPut
	case "/api/llm/config/test", "/api/master/chat", "/api/review/analysis":
		return r.Method == http.MethodPost
	default:
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	}
}

func allowedMethods(path string) string {
	switch path {
	case "/api/llm/config":
		return "GET, HEAD, PUT"
	case "/api/llm/config/test", "/api/master/chat", "/api/review/analysis":
		return "POST"
	default:
		return "GET, HEAD"
	}
}

func (h *handler) serveMarketContext(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid market context query")
		return
	}
	asOf := ""
	if dates, present := values["as_of"]; present {
		if len(dates) != 1 {
			writeError(w, r, http.StatusBadRequest, "as_of must be one date in YYYY-MM-DD format")
			return
		}
		parsed, err := time.Parse("2006-01-02", dates[0])
		if err != nil || parsed.Format("2006-01-02") != dates[0] {
			writeError(w, r, http.StatusBadRequest, "as_of must be one date in YYYY-MM-DD format")
			return
		}
		asOf = dates[0]
	}
	for key := range values {
		if key != "as_of" {
			writeError(w, r, http.StatusBadRequest, "unsupported market context parameter")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), contextRequestTimeout)
	defer cancel()
	db, err := storage.OpenContextView(h.config.DataDir, h.config.RequireCIFS)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "market context is not synchronized")
		return
	}
	if err != nil {
		h.log.Warn("Market context storage unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market context storage unavailable")
		return
	}
	defer db.Close()
	status, err := db.LoadSyncStatus(ctx)
	if errors.Is(err, fs.ErrNotExist) {
		writeError(w, r, http.StatusNotFound, "market context is not synchronized")
		return
	}
	if err != nil {
		h.log.Warn("Market context status unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market context is temporarily unavailable")
		return
	}
	if asOf == "" {
		asOf = status.AsOf
	}
	if asOf == "" {
		writeError(w, r, http.StatusNotFound, "market context is not synchronized")
		return
	}
	instruments, err := db.LoadInstruments(ctx)
	if err != nil {
		h.log.Warn("Market context instruments unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market context is temporarily unavailable")
		return
	}
	bars, err := db.LoadRecentBars(ctx, asOf, 21)
	if err != nil {
		h.log.Warn("Market context bars unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market context is temporarily unavailable")
		return
	}
	writeJSON(w, r, http.StatusOK, marketcontext.BuildSnapshot(asOf, instruments, bars, status))
}

func (h *handler) serveReviewDay(w http.ResponseWriter, r *http.Request) {
	asof, revision, err := parseReviewDayQuery(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "as_of and an optional revision are required")
		return
	}
	if h.review == nil {
		writeError(w, r, http.StatusServiceUnavailable, "review unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reviewRequestTimeout)
	defer cancel()
	day, err := h.review.Day(ctx, asof, revision)
	if err != nil {
		h.reviewError(w, r, ctx, err)
		return
	}
	writeJSON(w, r, http.StatusOK, day)
}

func (h *handler) serveReviewEvent(w http.ResponseWriter, r *http.Request) {
	asof, code, revision, err := parseReviewEventQuery(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "as_of, one stock code and an optional revision are required")
		return
	}
	if h.review == nil {
		writeError(w, r, http.StatusServiceUnavailable, "review unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reviewRequestTimeout)
	defer cancel()
	event, err := h.review.Event(ctx, asof, code, revision)
	if err != nil {
		h.reviewError(w, r, ctx, err)
		return
	}
	writeJSON(w, r, http.StatusOK, event)
}

func parseReviewDayQuery(r *http.Request) (string, string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", err
	}
	asof, err := oneDate(values, "as_of")
	if err != nil {
		return "", "", err
	}
	revision, err := optionalRevision(values)
	if err != nil {
		return "", "", err
	}
	for key := range values {
		if key != "as_of" && key != "revision" {
			return "", "", errors.New("unsupported review parameter")
		}
	}
	return asof, revision, nil
}

func parseReviewEventQuery(r *http.Request) (string, string, string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", "", err
	}
	asof, err := oneDate(values, "as_of")
	if err != nil {
		return "", "", "", err
	}
	codes := values["code"]
	if len(codes) != 1 || !config.ValidCode(codes[0]) {
		return "", "", "", errors.New("one stock code is required")
	}
	revision, err := optionalRevision(values)
	if err != nil {
		return "", "", "", err
	}
	for key := range values {
		if key != "as_of" && key != "revision" && key != "code" {
			return "", "", "", errors.New("unsupported review parameter")
		}
	}
	return asof, codes[0], revision, nil
}

func oneDate(values url.Values, key string) (string, error) {
	dates := values[key]
	if len(values[key]) != 1 || len(dates[0]) != len("2006-01-02") {
		return "", errors.New("invalid review date")
	}
	date, err := time.Parse("2006-01-02", dates[0])
	if err != nil || date.Format("2006-01-02") != dates[0] {
		return "", errors.New("invalid review date")
	}
	return dates[0], nil
}

func optionalRevision(values url.Values) (string, error) {
	revisions, present := values["revision"]
	if !present {
		return "", nil
	}
	if len(revisions) != 1 || !validRevision(revisions[0]) {
		return "", errors.New("invalid review revision")
	}
	return revisions[0], nil
}

func validRevision(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (h *handler) reviewError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	status, message := http.StatusServiceUnavailable, "review data is temporarily unavailable"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		status, message = http.StatusGatewayTimeout, "review request timed out"
	} else if errors.Is(err, fs.ErrNotExist) {
		status, message = http.StatusNotFound, "review report or event not found"
	}
	if status != http.StatusNotFound {
		h.log.Warn("Review data request failed", "status", status)
	}
	writeError(w, r, status, message)
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
