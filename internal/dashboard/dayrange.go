package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/dayrange"
)

type dayRangeResponse struct {
	SchemaVersion   int                         `json:"schema_version"`
	Date            string                      `json:"date"`
	Enabled         bool                        `json:"enabled"`
	Available       bool                        `json:"available"`
	Predictions     []dayrange.PredictionRecord `json:"predictions"`
	StockNames      map[string]string           `json:"stock_names"`
	RemainingModel  string                      `json:"remaining_model"`
	RemainingReason string                      `json:"remaining_reason,omitempty"`
	Issues          []string                    `json:"issues,omitempty"`
}

func (h *handler) serveDayRange(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid day-range query")
		return
	}
	if len(values) < 1 || len(values) > 2 {
		writeError(w, r, http.StatusBadRequest, "date and an optional stock code are required")
		return
	}
	date, err := dayRangeDate(values, true)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "date must be one date in YYYY-MM-DD format")
		return
	}
	code := ""
	if codes, present := values["code"]; present {
		if len(codes) != 1 || !h.poolCode(codes[0]) {
			writeError(w, r, http.StatusBadRequest, "code must be one stock in the configured pool")
			return
		}
		code = codes[0]
	}
	for key := range values {
		if key != "date" && key != "code" {
			writeError(w, r, http.StatusBadRequest, "unsupported day-range parameter")
			return
		}
	}
	response := dayRangeResponse{SchemaVersion: 1, Date: date, Enabled: h.config.DayRange.Enabled,
		Predictions: []dayrange.PredictionRecord{}, StockNames: map[string]string{}, RemainingModel: "unsupported", RemainingReason: "phase_six_not_implemented"}
	for _, stock := range h.stocks {
		response.StockNames[stock.Code] = stock.Name
	}
	if h.dayRange == nil {
		response.Issues = append(response.Issues, "dayrange_store_unavailable")
		writeJSON(w, r, http.StatusOK, response)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	records, err := h.dayRange.Predictions(ctx, date, code)
	if err != nil {
		h.dayRangeError(w, r, ctx, err, false)
		return
	}
	if records == nil {
		records = []dayrange.PredictionRecord{}
	}
	response.Available = true
	response.Predictions = records
	writeJSON(w, r, http.StatusOK, response)
}

func (h *handler) serveDayRangeStatus(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "day-range status does not accept query parameters")
		return
	}
	if h.dayRange == nil {
		status := dayrange.Status{SchemaVersion: 1, Enabled: h.config.DayRange.Enabled, Available: false,
			BaselineModelID: dayrange.BaselineModelID, Issues: []string{}}
		if h.config.DayRange.Enabled {
			status.Issues = append(status.Issues, "dayrange_store_unavailable")
		}
		writeJSON(w, r, http.StatusOK, status)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	status, err := h.dayRange.Status(ctx, "")
	if err != nil {
		h.dayRangeError(w, r, ctx, err, false)
		return
	}
	writeJSON(w, r, http.StatusOK, status)
}

func (h *handler) serveDayRangeEvaluation(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values) != 1 {
		writeError(w, r, http.StatusBadRequest, "one evaluation date is required")
		return
	}
	date, err := dayRangeDate(values, true)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "date must be one date in YYYY-MM-DD format")
		return
	}
	if h.dayRange == nil {
		writeError(w, r, http.StatusNotFound, "day-range evaluation not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	evaluation, err := h.dayRange.Evaluation(ctx, date)
	if err != nil {
		h.dayRangeError(w, r, ctx, err, true)
		return
	}
	writeJSON(w, r, http.StatusOK, evaluation)
}

func (h *handler) dayRangeError(w http.ResponseWriter, r *http.Request, ctx context.Context, err error, notFound bool) {
	if notFound && (errors.Is(err, sql.ErrNoRows) || errors.Is(err, fs.ErrNotExist)) {
		writeError(w, r, http.StatusNotFound, "day-range evaluation not found")
		return
	}
	status, message := http.StatusServiceUnavailable, "day-range data is temporarily unavailable"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		status, message = http.StatusGatewayTimeout, "day-range request timed out"
	}
	h.log.Warn("Day-range data request failed", "status", status)
	writeError(w, r, status, message)
}

func dayRangeDate(values url.Values, required bool) (string, error) {
	dates, present := values["date"]
	if !present {
		if required {
			return "", errors.New("date is required")
		}
		return "", nil
	}
	if len(dates) != 1 || len(dates[0]) != len(time.DateOnly) {
		return "", errors.New("invalid date")
	}
	parsed, err := time.Parse(time.DateOnly, dates[0])
	if err != nil || parsed.Format(time.DateOnly) != dates[0] {
		return "", errors.New("invalid date")
	}
	return dates[0], nil
}

func (h *handler) poolCode(code string) bool {
	for _, stock := range h.stocks {
		if stock.Code == code {
			return true
		}
	}
	return false
}
