package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const MinuteLimit = 640

type MinuteBar struct {
	End    time.Time `json:"end"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume_raw"`
	Final  bool      `json:"final"`
}

type MinuteHistory struct {
	Version    int         `json:"schema_version"`
	Code       string      `json:"code"`
	Name       string      `json:"name"`
	Source     string      `json:"source"`
	Period     int         `json:"period_minutes"`
	BasePeriod int         `json:"base_period_minutes"`
	Adjust     string      `json:"adjust"`
	VolumeUnit string      `json:"volume_unit"`
	FetchedAt  time.Time   `json:"fetched_at"`
	Bars       []MinuteBar `json:"bars"`
}

func MinutePeriod(period int) bool {
	return period == 5 || period == 15 || period == 30 || period == 60
}

// MinuteSession returns the session's opening minute. End timestamps must not
// straddle the lunch break or include the opening auction as a separate bar.
func MinuteSession(end time.Time, period int) (int, bool) {
	t := end.In(Shanghai)
	n := t.Hour()*60 + t.Minute()
	for _, start := range []int{570, 780} {
		if MinutePeriod(period) && n > start && n <= start+120 && (n-start)%period == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
			return start, true
		}
	}
	return 0, false
}

func (h MinuteHistory) Validate() error { return h.validateLimit(MinuteLimit) }

// ValidateBase accepts only the native five-minute series. Aggregated periods
// are derived on read and must never be persisted as if they were native data.
func (h MinuteHistory) ValidateBase() error {
	if h.Period != 5 || h.BasePeriod != 5 {
		return errors.New("minute history is not a native five-minute series")
	}
	return h.validateLimit(MinuteLimit)
}

func (h MinuteHistory) validateLimit(limit int) error {
	if h.Version != 1 || !config.ValidInstrumentCode(h.Code) || h.Source != "tencent" || !MinutePeriod(h.Period) || h.BasePeriod != 5 ||
		h.Adjust != "none" || h.VolumeUnit != "provider_raw" || h.FetchedAt.IsZero() || len(h.Bars) == 0 || len(h.Bars) > limit {
		return errors.New("invalid minute history metadata")
	}
	var previous time.Time
	for _, b := range h.Bars {
		_, session := MinuteSession(b.End, h.Period)
		if !session || !b.End.After(previous) || b.End.After(h.FetchedAt.Add(5*time.Minute)) || b.End.Weekday() == time.Saturday || b.End.Weekday() == time.Sunday {
			return errors.New("invalid minute time or ordering")
		}
		for _, v := range []float64{b.Open, b.High, b.Low, b.Close, b.Volume} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e12 {
				return errors.New("invalid minute numeric value")
			}
		}
		if b.Low <= 0 || b.High < math.Max(b.Open, b.Close) || b.Low > math.Min(b.Open, b.Close) || b.Final && b.End.After(h.FetchedAt) {
			return errors.New("invalid minute OHLC or completion")
		}
		previous = b.End
	}
	return nil
}

// ValidateMinutePair proves that an aggregated series and its native base
// series came from the same observation. The comparison is value based so a
// caller cannot relabel bars from another day, source or adjustment.
func ValidateMinutePair(base, derived MinuteHistory) error {
	if base.Period != 5 || derived.Period == 5 || derived.BasePeriod != 5 {
		return errors.New("invalid minute pair periods")
	}
	if base.Code != derived.Code || base.Source != derived.Source || base.Adjust != derived.Adjust ||
		base.VolumeUnit != derived.VolumeUnit || !base.FetchedAt.Equal(derived.FetchedAt) {
		return errors.New("minute pair metadata mismatch")
	}
	if err := base.Validate(); err != nil {
		return err
	}
	if err := derived.Validate(); err != nil {
		return err
	}
	aggregated := AggregateMinutes(base.Bars, derived.Period, base.FetchedAt)
	expected := make([]MinuteBar, 0, len(aggregated))
	for _, bar := range aggregated {
		if bar.Final {
			expected = append(expected, bar)
		}
	}
	if len(expected) != len(derived.Bars) {
		return errors.New("minute pair bar count mismatch")
	}
	for i := range expected {
		want, got := expected[i], derived.Bars[i]
		if !want.End.Equal(got.End) || want.Final != got.Final ||
			!near(want.Open, got.Open) || !near(want.High, got.High) || !near(want.Low, got.Low) ||
			!near(want.Close, got.Close) || !near(want.Volume, got.Volume) {
			return errors.New("minute pair aggregate mismatch")
		}
	}
	return nil
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-6 }

// AggregateMinutes derives only contiguous groups. An incomplete historical
// group is omitted; the currently forming group may contain a contiguous prefix.
// Completion comes from persisted upstream observations, never from wall time.
func AggregateMinutes(bars []MinuteBar, period int, observed time.Time) []MinuteBar {
	if !MinutePeriod(period) {
		return nil
	}
	if period == 5 {
		return append([]MinuteBar{}, bars...)
	}
	out := []MinuteBar{}
	for i := 0; i < len(bars); {
		first := bars[i]
		start, ok := MinuteSession(first.End, 5)
		if !ok {
			i++
			continue
		}
		t := first.End.In(Shanghai)
		minute := t.Hour()*60 + t.Minute()
		groupMinute := start + ((minute-start-1)/period+1)*period
		end := time.Date(t.Year(), t.Month(), t.Day(), groupMinute/60, groupMinute%60, 0, 0, Shanghai)
		group := first
		group.End = end
		valid := first.End.Equal(end.Add(-time.Duration(period-5) * time.Minute))
		j := i + 1
		for j < len(bars) && !bars[j].End.After(end) {
			b := bars[j]
			valid = valid && b.End.Equal(bars[j-1].End.Add(5*time.Minute))
			group.High = math.Max(group.High, b.High)
			group.Low = math.Min(group.Low, b.Low)
			group.Close = b.Close
			group.Volume += b.Volume
			group.Final = group.Final && b.Final
			j++
		}
		complete := j-i == period/5
		// A final prefix can still be part of an active larger interval. Only
		// emit a partial group at the very end; callers provide observed cutoff.
		if valid && (complete || j == len(bars) && end.After(observed)) {
			group.Final = group.Final && complete
			out = append(out, group)
		}
		i = j
	}
	return out
}

type MinuteClient struct {
	HTTP     *http.Client
	Endpoint string
	Now      func() time.Time
	pacer    requestPacer
}

func NewMinuteClient(c config.Config) *MinuteClient {
	return &MinuteClient{HTTP: newHTTPClient(config.Duration(c.Timeout)), Endpoint: c.Intraday.TencentEndpoint,
		Now: time.Now, pacer: requestPacer{interval: config.Duration(c.Intraday.RequestInterval)}}
}
func (c *MinuteClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *MinuteClient) FetchMinutes(ctx context.Context, stock config.Stock, count int) (MinuteHistory, error) {
	if !config.ValidInstrumentCode(stock.Code) || count < 1 || count > MinuteLimit {
		return MinuteHistory{}, errors.New("invalid minute request")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return MinuteHistory{}, errors.New("invalid minute endpoint")
	}
	q := u.Query()
	q.Set("param", stock.Code+",m5,,"+strconv.Itoa(count))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return MinuteHistory{}, errors.New("invalid minute request")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return MinuteHistory{}, err
	}
	// Use request start as the conservative completion watermark. A bar that
	// closes while this request is in flight is revalidated in the next poll.
	observed := c.Now().In(Shanghai)
	raw, err := readResponse(ctx, c.HTTP, req)
	if err != nil {
		return MinuteHistory{}, err
	}
	var envelope struct {
		Code *int                       `json:"code"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil || *envelope.Code != 0 {
		return MinuteHistory{}, &DataError{Reason: "invalid_minute_envelope"}
	}
	var payload struct {
		Rows [][]json.RawMessage `json:"m5"`
	}
	if json.Unmarshal(envelope.Data[stock.Code], &payload) != nil || len(payload.Rows) == 0 || len(payload.Rows) > count {
		return MinuteHistory{}, &DataError{Reason: "missing_or_invalid_minute_rows"}
	}
	h := MinuteHistory{Version: 1, Code: stock.Code, Name: stock.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: observed}
	for _, row := range payload.Rows {
		if len(row) < 6 {
			return MinuteHistory{}, &DataError{Reason: "invalid_minute_row"}
		}
		var stamp string
		if json.Unmarshal(row[0], &stamp) != nil {
			return MinuteHistory{}, &DataError{Reason: "invalid_minute_timestamp"}
		}
		end, err := time.ParseInLocation("200601021504", stamp, Shanghai)
		if err != nil || end.Format("200601021504") != stamp {
			return MinuteHistory{}, &DataError{Reason: "invalid_minute_timestamp"}
		}
		b := MinuteBar{End: end, Final: !end.After(observed.Add(-10 * time.Second))}
		for i, dest := range []*float64{&b.Open, &b.Close, &b.High, &b.Low, &b.Volume} {
			*dest, err = numeric(row[i+1])
			if err != nil {
				return MinuteHistory{}, err
			}
		}
		h.Bars = append(h.Bars, b)
	}
	if err := h.Validate(); err != nil {
		return MinuteHistory{}, &DataError{Reason: fmt.Sprint(err)}
	}
	return h, nil
}
