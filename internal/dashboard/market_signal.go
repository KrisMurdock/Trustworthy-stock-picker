package dashboard

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/marketforecast"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

func (h *handler) serveMarketSignal(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid market signal query")
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
			writeError(w, r, http.StatusBadRequest, "unsupported market signal parameter")
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
		h.log.Warn("Market signal storage unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market signal storage unavailable")
		return
	}
	defer db.Close()
	if asOf == "" {
		status, err := db.LoadSyncStatus(ctx)
		if errors.Is(err, fs.ErrNotExist) || (err == nil && status.AsOf == "") {
			writeError(w, r, http.StatusNotFound, "market context is not synchronized")
			return
		}
		if err != nil {
			h.log.Warn("Market signal status unavailable")
			writeError(w, r, http.StatusServiceUnavailable, "market signal is temporarily unavailable")
			return
		}
		asOf = status.AsOf
	}
	indices := marketforecast.DefaultIndices()
	codes := make([]string, 0, len(indices))
	for _, index := range indices {
		codes = append(codes, index.Code)
	}
	stored, err := db.LoadBarsThrough(ctx, codes, asOf)
	if err != nil {
		h.log.Warn("Market signal index history unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market signal is temporarily unavailable")
		return
	}
	minutes, err := db.LoadMinutesThrough(ctx, codes, asOf)
	if err != nil {
		h.log.Warn("Market signal index minutes unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market signal is temporarily unavailable")
		return
	}
	sessionMinutes, err := db.LoadSessionMinutesThrough(ctx, codes, asOf)
	if err != nil {
		h.log.Warn("Market signal index session minutes unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market signal is temporarily unavailable")
		return
	}
	koreaBars, err := db.LoadKoreaBarsThrough(ctx, []string{market.KoreaKOSPI, market.KoreaKOSDAQ}, asOf)
	if err != nil {
		h.log.Warn("Market signal korea history unavailable")
		writeError(w, r, http.StatusServiceUnavailable, "market signal is temporarily unavailable")
		return
	}
	series := make([]marketforecast.Series, 0, len(indices))
	for _, index := range indices {
		bars := make([]marketforecast.Bar, 0, len(stored[index.Code]))
		for _, bar := range stored[index.Code] {
			bars = append(bars, marketforecast.Bar{Date: bar.Date, Open: bar.Open, Close: bar.Close, High: bar.High, Low: bar.Low})
		}
		fiveMinutes := make([]marketforecast.MinuteBar, 0, len(minutes[index.Code]))
		for _, bar := range minutes[index.Code] {
			fiveMinutes = append(fiveMinutes, marketforecast.MinuteBar{
				End: bar.End, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
			})
		}
		thirtyMinutes := make([]marketforecast.MinuteBar, 0, len(sessionMinutes[index.Code]))
		for _, bar := range sessionMinutes[index.Code] {
			thirtyMinutes = append(thirtyMinutes, marketforecast.MinuteBar{
				End: bar.End, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume,
			})
		}
		series = append(series, marketforecast.Series{Index: index, Bars: bars, Minutes: fiveMinutes, SessionMinutes: thirtyMinutes})
	}
	korea := make([]marketforecast.KoreaSeries, 0, 2)
	for _, code := range []string{market.KoreaKOSPI, market.KoreaKOSDAQ} {
		bars := make([]marketforecast.Bar, 0, len(koreaBars[code]))
		for _, bar := range koreaBars[code] {
			bars = append(bars, marketforecast.Bar{Date: bar.Date, Open: bar.Open, Close: bar.Close, High: bar.High, Low: bar.Low})
		}
		korea = append(korea, marketforecast.KoreaSeries{Code: code, Bars: bars})
	}
	report, err := marketforecast.Build(series, marketforecast.Options{
		AsOf: asOf, NextTradingDay: calendar.New(h.config).Next, Korea: korea,
	})
	if err != nil {
		h.log.Warn("Market signal calculation failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, "market signal calculation failed")
		return
	}
	writeJSON(w, r, http.StatusOK, report)
}
