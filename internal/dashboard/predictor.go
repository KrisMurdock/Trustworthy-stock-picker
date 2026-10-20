package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/predictor"
)

const predictorBodyLimit = 4 << 10
const predictorConfigAction = "predictor-config"

// PredictorController is the minimal dashboard surface for the experimental
// five-minute forecaster. Implementations are optional so a dashboard built
// without a running predictor keeps its previous read-only behavior.
type PredictorController interface {
	View(context.Context) (predictor.View, error)
	History(context.Context, string) ([]predictor.Prediction, error)
	SetPushEnabled(context.Context, bool) error
}

type predictorConfigUpdate struct {
	PushEnabled *bool `json:"push_enabled"`
}

func (h *handler) servePredictions(w http.ResponseWriter, r *http.Request) {
	date, err := parsePredictionDate(r)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "date must be one value in YYYY-MM-DD format")
		return
	}
	if h.predictor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "predictor unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	history, err := h.predictor.History(ctx, date)
	if err != nil {
		h.predictorReadError(w, r, ctx, err)
		return
	}
	if history == nil {
		history = []predictor.Prediction{}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"date": date, "predictions": history})
}

func (h *handler) servePredictor(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "predictor does not accept query parameters")
		return
	}
	if h.predictor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "predictor unavailable")
		return
	}
	view, err := h.predictorView(r)
	if err != nil {
		h.predictorReadError(w, r, r.Context(), err)
		return
	}
	writeJSON(w, r, http.StatusOK, view)
}

func (h *handler) servePredictorConfig(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "predictor configuration does not accept query parameters")
		return
	}
	if h.predictor == nil {
		writeError(w, r, http.StatusServiceUnavailable, "predictor unavailable")
		return
	}
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", allowedMethods(r.URL.Path))
		writeError(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowPredictorRequest(r, predictorConfigAction) {
		writeError(w, r, http.StatusForbidden, "predictor configuration is not writable from this request")
		return
	}
	var update predictorConfigUpdate
	if err := decodePredictorUpdate(r, &update); err != nil || update.PushEnabled == nil {
		writeError(w, r, http.StatusBadRequest, "invalid predictor configuration")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if err := h.predictor.SetPushEnabled(ctx, *update.PushEnabled); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "predictor configuration is not writable")
		return
	}
	view, err := h.predictorView(r)
	if err != nil {
		h.predictorReadError(w, r, ctx, err)
		return
	}
	writeJSON(w, r, http.StatusOK, view)
}

func (h *handler) predictorView(r *http.Request) (predictor.View, error) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	view, err := h.predictor.View(ctx)
	if err != nil {
		return predictor.View{}, err
	}
	view.WriteMode = h.config.Predictor.WriteMode
	view.WriteEnabled = h.predictorSourceAllowed(r)
	return view, nil
}

func (h *handler) predictorReadError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error) {
	status, message := http.StatusServiceUnavailable, "predictor data is temporarily unavailable"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		status, message = http.StatusGatewayTimeout, "predictor request timed out"
	}
	h.log.Warn("Dashboard predictor request failed", "status", status)
	writeError(w, r, status, message)
}

func parsePredictionDate(r *http.Request) (string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values) != 1 || len(values["date"]) != 1 {
		return "", errors.New("invalid prediction date")
	}
	date := values.Get("date")
	if len(date) != len("2006-01-02") {
		return "", errors.New("invalid prediction date")
	}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil || parsed.Format("2006-01-02") != date {
		return "", errors.New("invalid prediction date")
	}
	return date, nil
}

func decodePredictorUpdate(r *http.Request, update *predictorConfigUpdate) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, predictorBodyLimit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(update); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("multiple predictor configuration documents")
	}
	return nil
}

func (h *handler) allowPredictorRequest(r *http.Request, action string) bool {
	if !h.predictorSourceAllowed(r) {
		return false
	}
	if r.Header.Get("X-Stock-Watcher-Action") != action {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, r.Host) {
			return false
		}
	}
	return true
}

func (h *handler) predictorSourceAllowed(r *http.Request) bool {
	switch h.config.Predictor.WriteMode {
	case "off":
		return false
	case "all":
		return true
	case "loopback":
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(strings.Trim(host, "[]"))
		return ip != nil && ip.IsLoopback()
	default:
		return false
	}
}
