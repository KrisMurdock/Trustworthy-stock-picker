package market

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const KoreaMinuteRangeLimit = 8 * 24 * time.Hour

var Korea = time.FixedZone("KST", 9*60*60)

// KoreaMinuteBar is one native one-minute KOSPI/KOSDAQ index observation.
// Naver exposes only the most recent sessions, so this cache is forward-only
// and must not be treated as a complete historical series.
type KoreaMinuteBar struct {
	End    time.Time `json:"end"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume_raw"`
	Final  bool      `json:"final"`
}

type NaverKoreaMinuteClient struct {
	HTTP     *http.Client
	Endpoint string
	Now      func() time.Time
	pacer    requestPacer
}

func NewNaverKoreaMinuteClient(timeout, interval time.Duration) *NaverKoreaMinuteClient {
	client := &NaverKoreaMinuteClient{
		HTTP: newHTTPClient(timeout), Endpoint: "https://api.stock.naver.com/chart/domestic/index",
		Now: time.Now, pacer: requestPacer{interval: interval},
	}
	return client
}

func (c *NaverKoreaMinuteClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *NaverKoreaMinuteClient) FetchMinutes(ctx context.Context, code string, from, through time.Time) ([]KoreaMinuteBar, error) {
	if !ValidKoreaCode(code) {
		return nil, &DataError{Reason: "invalid_korea_minute_code"}
	}
	from = from.In(Korea)
	through = through.In(Korea)
	if from.IsZero() || through.IsZero() || !through.After(from) || through.Sub(from) > KoreaMinuteRangeLimit {
		return nil, &DataError{Reason: "invalid_korea_minute_range"}
	}
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + "/" + code + "/minute")
	if err != nil {
		return nil, errors.New("invalid korea minute endpoint")
	}
	q := u.Query()
	q.Set("startDateTime", from.Format("20060102150405"))
	q.Set("endDateTime", through.Format("20060102150405"))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid korea minute request")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return nil, err
	}
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	return parseNaverKoreaMinutes(raw, from, through, now.In(Korea))
}

func parseNaverKoreaMinutes(raw []byte, from, through, observed time.Time) ([]KoreaMinuteBar, error) {
	var rows []struct {
		LocalDateTime            string  `json:"localDateTime"`
		CurrentPrice             float64 `json:"currentPrice"`
		OpenPrice                float64 `json:"openPrice"`
		HighPrice                float64 `json:"highPrice"`
		LowPrice                 float64 `json:"lowPrice"`
		AccumulatedTradingVolume float64 `json:"accumulatedTradingVolume"`
	}
	if !json.Valid(raw) || json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
		return nil, &DataError{Reason: "empty_korea_minutes"}
	}
	out := make([]KoreaMinuteBar, 0, len(rows))
	previous := time.Time{}
	for _, row := range rows {
		end, err := time.ParseInLocation("20060102150405", row.LocalDateTime, Korea)
		if err != nil || end.Before(from) || end.After(through) || end.Weekday() == time.Saturday || end.Weekday() == time.Sunday {
			return nil, &DataError{Reason: "invalid_korea_minute_time"}
		}
		if !end.After(previous) {
			return nil, &DataError{Reason: "invalid_korea_minute_ordering"}
		}
		previous = end
		minute := end.Hour()*60 + end.Minute()
		if minute < 9*60 || minute > 15*60+30 {
			return nil, &DataError{Reason: "invalid_korea_minute_session"}
		}
		bar := KoreaMinuteBar{
			End: end, Open: row.OpenPrice, High: row.HighPrice, Low: row.LowPrice,
			Close: row.CurrentPrice, Volume: row.AccumulatedTradingVolume,
			Final: !end.After(observed.Add(-time.Minute)),
		}
		if bar.Open <= 0 || bar.Close <= 0 || bar.Low <= 0 || bar.High < bar.Open || bar.High < bar.Close || bar.Low > bar.Open || bar.Low > bar.Close || bar.Volume < 0 {
			return nil, &DataError{Reason: "invalid_korea_minute_row"}
		}
		if bar.Final {
			out = append(out, bar)
		}
	}
	if len(out) == 0 {
		return nil, &DataError{Reason: "no_final_korea_minutes"}
	}
	return out, nil
}
