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
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
)

const HKMinuteWindowDays = 62
const hkMinuteHistoryLimit = 5000

type HKMinuteHistory struct {
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

type HKMinuteBar struct {
	MinuteBar
	TradingMinutes int `json:"trading_minutes"`
}

func HKMinuteSlot(end time.Time) (int, bool) {
	local := end.In(Shanghai)
	if local.Second() != 0 || local.Nanosecond() != 0 {
		return 0, false
	}
	minute := local.Hour()*60 + local.Minute()
	switch {
	case minute > 570 && minute <= 720 && (minute-570)%5 == 0:
		return (minute-570)/5 - 1, true
	case minute > 780 && minute <= 960 && (minute-780)%5 == 0:
		return 30 + (minute-780)/5 - 1, true
	default:
		return 0, false
	}
}

func (h HKMinuteHistory) ValidateBase() error {
	if h.Version != 1 || !config.ValidHKStockCode(h.Code) || h.Source != "eastmoney_hk" || h.Period != 5 || h.BasePeriod != 5 ||
		h.Adjust != "none" || h.VolumeUnit != "provider_raw" || h.FetchedAt.IsZero() || len(h.Bars) == 0 || len(h.Bars) > hkMinuteHistoryLimit {
		return errors.New("invalid HK minute history metadata")
	}
	var previous time.Time
	for _, bar := range h.Bars {
		local := bar.End.In(Shanghai)
		_, valid := HKMinuteSlot(local)
		if !valid || !bar.End.After(previous) || bar.End.After(h.FetchedAt.Add(5*time.Minute)) || local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
			return errors.New("invalid HK minute time or ordering")
		}
		for _, value := range []float64{bar.Open, bar.High, bar.Low, bar.Close, bar.Volume} {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e12 {
				return errors.New("invalid HK minute numeric value")
			}
		}
		if bar.Low <= 0 || bar.High < math.Max(bar.Open, bar.Close) || bar.Low > math.Min(bar.Open, bar.Close) || bar.Final && bar.End.After(h.FetchedAt) {
			return errors.New("invalid HK minute OHLC or completion")
		}
		previous = bar.End
	}
	return nil
}

func hkMinuteEnd(date time.Time, slot int) time.Time {
	date = date.In(Shanghai)
	minute := 570 + (slot+1)*5
	if slot >= 30 {
		minute = 780 + (slot-30+1)*5
	}
	return time.Date(date.Year(), date.Month(), date.Day(), minute/60, minute%60, 0, 0, Shanghai)
}

func AggregateHKMinutes(bars []MinuteBar, period int, observed time.Time, earlyCloses map[string]bool) []HKMinuteBar {
	if !MinutePeriod(period) {
		return nil
	}
	result := make([]HKMinuteBar, 0, len(bars)*5/period+1)
	groupSize := period / 5
	for index := 0; index < len(bars); {
		bar := bars[index]
		slot, valid := HKMinuteSlot(bar.End)
		if !valid {
			index++
			continue
		}
		date := bar.End.In(Shanghai).Format("2006-01-02")
		daySlots := 66
		if earlyCloses[date] {
			daySlots = 30
		}
		if slot >= daySlots {
			index++
			continue
		}
		start := slot / groupSize * groupSize
		stop := min(start+groupSize, daySlots)
		end := hkMinuteEnd(bar.End, stop-1)
		group := HKMinuteBar{MinuteBar: MinuteBar{End: end, Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume, Final: bar.Final}, TradingMinutes: 5}
		valid = slot == start
		next := index + 1
		for next < len(bars) {
			candidate := bars[next]
			candidateSlot, candidateValid := HKMinuteSlot(candidate.End)
			if !candidateValid || candidate.End.In(Shanghai).Format("2006-01-02") != date || candidateSlot < start || candidateSlot >= stop {
				break
			}
			if candidateSlot != slot+1 {
				valid = false
			}
			slot = candidateSlot
			group.High = math.Max(group.High, candidate.High)
			group.Low = math.Min(group.Low, candidate.Low)
			group.Close = candidate.Close
			group.Volume += candidate.Volume
			group.Final = group.Final && candidate.Final
			group.TradingMinutes += 5
			next++
		}
		complete := valid && slot == stop-1 && group.TradingMinutes == (stop-start)*5
		forming := valid && next == len(bars) && end.After(observed)
		if complete || forming {
			group.Final = group.Final && complete
			result = append(result, group)
		}
		index = next
	}
	return result
}

type HKMinuteClient struct {
	HTTP     *http.Client
	Endpoint string
	Now      func() time.Time
	pacer    requestPacer
}

func NewHKMinuteClient(timeout, interval time.Duration, endpoint string) *HKMinuteClient {
	return &HKMinuteClient{HTTP: newHTTPClient(timeout), Endpoint: endpoint, Now: time.Now, pacer: requestPacer{interval: interval}}
}

func (c *HKMinuteClient) Close() { c.HTTP.CloseIdleConnections() }

func (c *HKMinuteClient) FetchMinutes(ctx context.Context, stock config.Stock, from, through string) (HKMinuteHistory, error) {
	if c == nil || c.HTTP == nil || !config.ValidHKStockCode(stock.Code) {
		return HKMinuteHistory{}, errors.New("invalid HK minute request")
	}
	first, firstErr := time.ParseInLocation("2006-01-02", from, Shanghai)
	last, lastErr := time.ParseInLocation("2006-01-02", through, Shanghai)
	now := time.Now().In(Shanghai)
	if c.Now != nil {
		now = c.Now().In(Shanghai)
	}
	if firstErr != nil || lastErr != nil || first.Format("2006-01-02") != from || last.Format("2006-01-02") != through ||
		first.After(last) || last.Sub(first) > HKMinuteWindowDays*24*time.Hour || through > now.Format("2006-01-02") {
		return HKMinuteHistory{}, errors.New("invalid HK minute range")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return HKMinuteHistory{}, errors.New("invalid HK minute endpoint")
	}
	query := u.Query()
	query.Set("fields1", "f1,f2,f3,f4,f5,f6")
	query.Set("fields2", "f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61")
	query.Set("ut", "bd1d9ddb04089700cf9c27f6f7426281")
	query.Set("klt", "5")
	query.Set("fqt", "0")
	query.Set("secid", "116."+stock.Code[2:])
	query.Set("beg", strings.ReplaceAll(from, "-", ""))
	query.Set("end", strings.ReplaceAll(through, "-", ""))
	u.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return HKMinuteHistory{}, errors.New("invalid HK minute request")
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 stock-watcher/1.0")
	if err := c.pacer.wait(ctx); err != nil {
		return HKMinuteHistory{}, err
	}
	observed := time.Now().In(Shanghai)
	if c.Now != nil {
		observed = c.Now().In(Shanghai)
	}
	raw, err := readResponse(ctx, c.HTTP, request)
	if err != nil {
		return HKMinuteHistory{}, err
	}
	var response struct {
		RC   *int `json:"rc"`
		Data *struct {
			Code   string   `json:"code"`
			Market *int     `json:"market"`
			Name   string   `json:"name"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil || response.RC == nil || *response.RC != 0 || response.Data == nil {
		return HKMinuteHistory{}, &DataError{Reason: "invalid_hk_minute_envelope"}
	}
	data := response.Data
	if data.Code != stock.Code[2:] || data.Market == nil || *data.Market != 116 {
		return HKMinuteHistory{}, &DataError{Reason: "hk_minute_stock_mismatch"}
	}
	if len(data.Klines) == 0 || len(data.Klines) > hkMinuteHistoryLimit {
		return HKMinuteHistory{}, &DataError{Reason: "empty_or_oversized_hk_minutes"}
	}
	name := stock.Name
	if name == "" {
		name = data.Name
	}
	history := HKMinuteHistory{Version: 1, Code: stock.Code, Name: name, Source: "eastmoney_hk", Period: 5, BasePeriod: 5,
		Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: observed, Bars: make([]MinuteBar, 0, len(data.Klines))}
	for _, line := range data.Klines {
		row := strings.Split(line, ",")
		if len(row) != 11 {
			return HKMinuteHistory{}, &DataError{Reason: "invalid_hk_minute_row"}
		}
		end, err := time.ParseInLocation("2006-01-02 15:04", row[0], Shanghai)
		if err != nil || end.Format("2006-01-02 15:04") != row[0] || end.Format("2006-01-02") < from || end.Format("2006-01-02") > through {
			return HKMinuteHistory{}, &DataError{Reason: "invalid_hk_minute_timestamp"}
		}
		bar := MinuteBar{End: end, Final: !end.After(observed.Add(-10 * time.Second))}
		if end.Format("15:04") == "12:00" || end.Format("15:04") == "16:00" {
			bar.Final = !end.Add(10 * time.Minute).After(observed)
		}
		for index, destination := range []*float64{&bar.Open, &bar.Close, &bar.High, &bar.Low, &bar.Volume} {
			value, err := strconv.ParseFloat(row[index+1], 64)
			if err != nil {
				return HKMinuteHistory{}, &DataError{Reason: "invalid_hk_minute_numeric"}
			}
			*destination = value
		}
		history.Bars = append(history.Bars, bar)
	}
	if err := history.ValidateBase(); err != nil {
		return HKMinuteHistory{}, &DataError{Reason: fmt.Sprint(err)}
	}
	return history, nil
}
