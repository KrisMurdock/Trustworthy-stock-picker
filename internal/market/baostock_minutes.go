package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const baoMinuteFields = "date,time,code,open,high,low,close,volume,amount,adjustflag"
const baoMinutePageSize = 400

// MinuteArchive is a provider-neutral historical minute range. Unlike the
// realtime MinuteHistory contract, it is not bounded by the provider's 640-bar
// page limit and must be imported as archived data.
type MinuteArchive struct {
	Version    int         `json:"schema_version"`
	Code       string      `json:"code"`
	Name       string      `json:"name"`
	Source     string      `json:"source"`
	Period     int         `json:"period_minutes"`
	BasePeriod int         `json:"base_period_minutes"`
	Adjust     string      `json:"adjust"`
	VolumeUnit string      `json:"volume_unit"`
	From       string      `json:"from"`
	Through    string      `json:"through"`
	FetchedAt  time.Time   `json:"fetched_at"`
	Bars       []MinuteBar `json:"bars"`
}

func (h MinuteArchive) Validate() error {
	first, e1 := time.ParseInLocation("2006-01-02", h.From, Shanghai)
	last, e2 := time.ParseInLocation("2006-01-02", h.Through, Shanghai)
	if h.Version != 1 || !config.ValidCode(h.Code) || (h.Source != "baostock" && h.Source != "sina") || h.Period != 5 ||
		h.BasePeriod != 5 || h.Adjust != "none" || (h.VolumeUnit != "shares" && h.VolumeUnit != "provider_raw") || h.FetchedAt.IsZero() ||
		e1 != nil || e2 != nil || first.After(last) || len(h.Bars) > MinuteArchiveLimit {
		return errors.New("invalid minute archive metadata")
	}
	var previous time.Time
	for _, b := range h.Bars {
		day := b.End.In(Shanghai).Format("2006-01-02")
		if day < h.From || day > h.Through || !b.Final || !b.End.After(previous) || !validArchiveMinuteBar(b) {
			return errors.New("invalid minute archive bar")
		}
		previous = b.End
	}
	return nil
}

func validArchiveMinuteBar(b MinuteBar) bool {
	if _, ok := MinuteSession(b.End, 5); !ok || b.End.Weekday() == time.Saturday || b.End.Weekday() == time.Sunday {
		return false
	}
	for _, v := range []float64{b.Open, b.High, b.Low, b.Close, b.Volume} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e12 {
			return false
		}
	}
	return b.Low > 0 && b.High >= math.Max(b.Open, b.Close) && b.Low <= math.Min(b.Open, b.Close)
}

// FetchMinuteRange downloads native five-minute bars through BaoStock. The
// public endpoint closes a session after one historical query, so every page
// owns a fresh TCP session. 500-row pages reset the connection; 400 is the
// largest verified page size.
func (c *BaoStockClient) FetchMinuteRange(ctx context.Context, stock config.Stock, from, through string) (MinuteArchive, error) {
	h := MinuteArchive{
		Version: 1, Code: stock.Code, Name: stock.Name, Source: "baostock", Period: 5,
		BasePeriod: 5, Adjust: "none", VolumeUnit: "shares", From: from, Through: through,
		FetchedAt: time.Now().In(Shanghai),
	}
	if !config.ValidCode(stock.Code) {
		return h, &DataError{Reason: "invalid_minute_archive_stock"}
	}
	first, e1 := time.ParseInLocation("2006-01-02", from, Shanghai)
	last, e2 := time.ParseInLocation("2006-01-02", through, Shanghai)
	if e1 != nil || e2 != nil || first.After(last) || last.Sub(first) > 20*366*24*time.Hour {
		return h, &DataError{Reason: "invalid_minute_archive_range"}
	}
	code := stock.Code[:2] + "." + stock.Code[2:]
	bars, err := c.fetchMinutePages(ctx, code, from, through)
	if err != nil {
		return MinuteArchive{}, err
	}
	h.Bars = bars
	if len(h.Bars) > MinuteArchiveLimit {
		return MinuteArchive{}, &DataError{Reason: "minute_archive_exceeds_limit"}
	}
	if err := h.Validate(); err != nil {
		return MinuteArchive{}, &DataError{Reason: err.Error()}
	}
	return h, nil
}

func (c *BaoStockClient) fetchMinutePages(ctx context.Context, code, pageStart, pageEnd string) ([]MinuteBar, error) {
	var bars []MinuteBar
	complete := false
	for page := 1; page <= minutePageLimit(pageStart, pageEnd); page++ {
		pageBars, err := c.fetchMinutePage(ctx, code, pageStart, pageEnd, page)
		if err != nil {
			return nil, fmt.Errorf("baostock minutes %s page %d: %w", pageStart, page, err)
		}
		bars = append(bars, pageBars...)
		if len(pageBars) < baoMinutePageSize {
			complete = true
			break
		}
	}
	if !complete {
		return nil, &DataError{Reason: "baostock_minute_exceeds_limit"}
	}
	return bars, nil
}

func minutePageLimit(pageStart, pageEnd string) int {
	first, e1 := time.ParseInLocation("2006-01-02", pageStart, Shanghai)
	last, e2 := time.ParseInLocation("2006-01-02", pageEnd, Shanghai)
	if e1 != nil || e2 != nil || first.After(last) {
		return 0
	}
	days := int(last.Sub(first).Hours()/24) + 1
	pages := (days*48 + baoMinutePageSize - 1) / baoMinutePageSize
	if pages < 3 {
		pages = 3
	}
	return pages + 2
}

func (c *BaoStockClient) fetchMinutePage(ctx context.Context, code, pageStart, pageEnd string, page int) ([]MinuteBar, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			if err := sleepContext(ctx, time.Duration(attempt)*time.Second); err != nil {
				return nil, err
			}
		}
		s, cleanup, err := c.openSession(ctx)
		if err != nil {
			lastErr = err
			if !retryBaoRequest(err) {
				return nil, err
			}
			continue
		}
		bars, err := c.fetchMinutePageSession(ctx, s, code, pageStart, pageEnd, page)
		cleanup()
		if err == nil {
			return bars, nil
		}
		lastErr = err
		if !retryBaoRequest(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *BaoStockClient) fetchMinutePageSession(ctx context.Context, s *baoSession, code, pageStart, pageEnd string, page int) ([]MinuteBar, error) {
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	body := strings.Join([]string{
		"query_history_k_data_plus", "anonymous", strconv.Itoa(page), strconv.Itoa(baoMinutePageSize),
		code, baoMinuteFields, pageStart, pageEnd, "5", "3",
	}, baoSeparator)
	parts, err := s.exchange(ctx, "95", "96", body)
	if err != nil {
		return nil, err
	}
	if len(parts) < 13 || parts[2] != "query_history_k_data_plus" || parts[3] != "anonymous" ||
		parts[4] != strconv.Itoa(page) || parts[5] != strconv.Itoa(baoMinutePageSize) || parts[7] != code ||
		parts[8] != baoMinuteFields || parts[9] != pageStart || parts[10] != pageEnd ||
		parts[11] != "5" || parts[12] != "3" {
		return nil, &DataError{Reason: "baostock_minute_query_mismatch"}
	}
	var data struct {
		Records [][]string `json:"record"`
	}
	if parts[6] != "" && json.Unmarshal([]byte(parts[6]), &data) != nil {
		return nil, &DataError{Reason: "invalid_baostock_minute_records"}
	}
	if len(data.Records) > baoMinutePageSize {
		return nil, &DataError{Reason: "invalid_baostock_minute_page_size"}
	}
	bars := make([]MinuteBar, 0, len(data.Records))
	for _, row := range data.Records {
		bar, err := parseBaoMinuteRow(row, code, pageStart, pageEnd)
		if err != nil {
			return nil, err
		}
		bars = append(bars, bar)
	}
	return bars, nil
}

func retryBaoRequest(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var failure *Failure
	return errors.As(err, &failure) && failure.Retryable()
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseBaoMinuteRow(row []string, code, pageStart, pageEnd string) (MinuteBar, error) {
	if len(row) != 10 || row[1] == "" || row[2] != code || row[0] < pageStart || row[0] > pageEnd || row[9] != "3" {
		return MinuteBar{}, &DataError{Reason: "invalid_baostock_minute_row"}
	}
	end, err := parseBaoMinuteTimestamp(row[1])
	if err != nil || end.In(Shanghai).Format("2006-01-02") != row[0] {
		return MinuteBar{}, &DataError{Reason: "invalid_baostock_minute_timestamp"}
	}
	bar := MinuteBar{End: end, Final: true}
	for i, dest := range []*float64{&bar.Open, &bar.High, &bar.Low, &bar.Close, &bar.Volume} {
		*dest, err = numeric(json.RawMessage(row[i+3]))
		if err != nil {
			return MinuteBar{}, err
		}
	}
	if _, err := numeric(json.RawMessage(row[8])); err != nil {
		return MinuteBar{}, err
	}
	if !validArchiveMinuteBar(bar) {
		return MinuteBar{}, &DataError{Reason: "invalid_baostock_minute_ohlc"}
	}
	return bar, nil
}

func parseBaoMinuteTimestamp(value string) (time.Time, error) {
	if len(value) < 14 {
		return time.Time{}, errors.New("short timestamp")
	}
	stamp := value[:14]
	for _, r := range value {
		if r < '0' || r > '9' {
			return time.Time{}, errors.New("non-numeric timestamp")
		}
	}
	end, err := time.ParseInLocation("20060102150405", stamp, Shanghai)
	if err != nil || end.Format("20060102150405") != stamp {
		return time.Time{}, errors.New("invalid timestamp")
	}
	return end, nil
}
