package maexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Price decodes the frozen reference's SQLite row representation. Prices are
// millionths of yuan; volume is shares. No query or network access is performed.
type Price struct {
	Adjust, Date           string
	Open, High, Low, Close int64
	Volume                 float64
	Source                 string
}

func (p *Price) UnmarshalJSON(raw []byte) error {
	var fields []json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return e
	}
	if len(fields) != 8 {
		return errors.New("price row requires eight fields")
	}
	for i, v := range []any{&p.Adjust, &p.Date, &p.Open, &p.High, &p.Low, &p.Close, &p.Volume, &p.Source} {
		if e := json.Unmarshal(fields[i], v); e != nil {
			return e
		}
	}
	return nil
}
func (p Price) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{p.Adjust, p.Date, p.Open, p.High, p.Low, p.Close, p.Volume, p.Source})
}

type Snapshot struct {
	Version  int                `json:"schema_version"`
	Calendar []string           `json:"calendar"`
	Stocks   map[string][]Price `json:"stocks"`
}

func Prepare(ctx context.Context, s Snapshot, sessions []string, signals []Signal, c Config, limitMode string) (Input, map[string]int, error) {
	in := Input{Version: 3, PriceBasis: "qfq_proxy", Model: "adjusted_return_proxy", Config: c, Provenance: Provenance{Calendar: "frozen snapshot calendar", Rules: "assumed board bands and missing/volume guards", Assumptions: "qfq synthetic units; no historical ST/corporate-action ledger; approximate fees and capacity"}, Sessions: append([]string{}, sessions...), Signals: append([]Signal{}, signals...), Bars: map[string]map[string]Bar{}}
	audit := map[string]int{}
	if s.Version != 1 || (limitMode != "board" && limitMode != "five_percent" && limitMode != "historical_board") {
		return in, audit, errors.New("invalid snapshot version or limit mode")
	}
	dates := map[string]bool{}
	for i, d := range s.Calendar {
		if !DateOK(d) || (i > 0 && d <= s.Calendar[i-1]) {
			return in, audit, errors.New("invalid snapshot calendar")
		}
		dates[d] = true
	}
	prior := map[string]bool{}
	for i, d := range sessions {
		if !dates[d] || (i > 0 && d <= sessions[i-1]) {
			return in, audit, errors.New("sessions outside frozen calendar or unordered")
		}
		in.Bars[d] = map[string]Bar{}
		prior[d] = true
	}
	for _, ps := range s.Stocks {
		for _, p := range ps {
			if e := ctx.Err(); e != nil {
				return in, audit, e
			}
			if !DateOK(p.Date) || (p.Adjust != "qfq" && p.Adjust != "none") || p.Source == "" || p.Open <= 0 || p.Close <= 0 || p.Low <= 0 || p.High < p.Open || p.High < p.Close || p.Low > p.Open || p.Low > p.Close || !finite(p.Volume) || p.Volume < 0 {
				return in, audit, errors.New("invalid frozen price row")
			}
			if p.Adjust == "qfq" {
				prior[p.Date] = true
			}
		}
	}
	priorDates := keys(prior)
	if limitMode == "historical_board" {
		priorDates = append([]string{}, s.Calendar...)
	}
	dayIndex := map[string]int{}
	for i, d := range priorDates {
		dayIndex[d] = i
	}
	codes := map[string]bool{}
	for _, sig := range signals {
		codes[sig.Code] = true
	}
	for _, code := range keys(codes) {
		if e := ctx.Err(); e != nil {
			return in, audit, e
		}
		records, ok := s.Stocks[code]
		if !ok {
			return in, audit, fmt.Errorf("missing snapshot stock %s", code)
		}
		q, raw := map[string]Price{}, map[string]Price{}
		all := map[string]bool{}
		for _, d := range sessions {
			all[d] = true
		}
		for _, p := range records {
			dest := raw
			if p.Adjust == "qfq" {
				dest = q
				all[p.Date] = true
			}
			if _, ok := dest[p.Date]; ok {
				return in, audit, errors.New("duplicate frozen price")
			}
			dest[p.Date] = p
		}
		qds := make([]string, 0, len(q))
		for d := range q {
			qds = append(qds, d)
		}
		sort.Strings(qds)
		qi := map[string]int{}
		for i, d := range qds {
			qi[d] = i
		}
		amountPrefix := make([]float64, len(priorDates)+1)
		for i, day := range priorDates {
			p := raw[day]
			amountPrefix[i+1] = amountPrefix[i] + float64(p.Close)/1e6*p.Volume
		}
		last := 0.0
		for _, d := range keys(all) {
			p, seen := q[d]
			if _, ok := in.Bars[d]; !ok {
				if seen {
					last = float64(p.Close) / 1e6
				}
				continue
			}
			prev := last
			o, h, l, close := last, last, last, last
			if seen {
				o, h, l, close = float64(p.Open)/1e6, float64(p.High)/1e6, float64(p.Low)/1e6, float64(p.Close)/1e6
				last = close
			} else if last == 0 {
				o, h, l, close = 1, 1, 1, 1
			}
			index := -1
			if seen {
				index = qi[d]
			}
			ma := close
			if index >= 9 {
				sum := int64(0)
				for _, qd := range qds[index-9 : index+1] {
					if q[qd].Close > math.MaxInt64/10 {
						return in, audit, errors.New("price sum overflow")
					}
					sum += q[qd].Close
				}
				ma = float64(sum) / 1e7
			}
			pct := .1
			if strings.HasPrefix(code, "sh68") || (strings.HasPrefix(code, "sz30") && (limitMode != "historical_board" || d >= "2020-08-24")) {
				pct = .2
			}
			if limitMode == "five_percent" {
				pct = .05
			}
			ref := prev
			if ref == 0 {
				ref = close
			}
			lower, upper := ref*(1-pct), ref*(1+pct)
			anomalous := seen && (h > upper*1.005 || l < lower*.995)
			usable := seen && p.Volume > 0 && prev > 0
			adv := 0.0
			idx := dayIndex[d]
			if idx >= 20 {
				for _, pd := range priorDates[idx-20 : idx] {
					rp := raw[pd]
					adv += float64(rp.Close) / 1e6 * rp.Volume
				}
				adv /= 20
				if limitMode == "historical_board" {
					adv = (amountPrefix[idx] - amountPrefix[idx-20]) / 20
				}
			}
			observation := "observed"
			if !seen {
				observation = "carried_missing"
				if prev == 0 {
					observation = "unpriced_before_first"
				}
			}
			audit[observation]++
			if anomalous {
				audit["abnormal_band_days"]++
			}
			if seen && p.Volume <= 0 {
				audit["zero_volume_days"]++
			}
			transfer, stamp := .00001, .0005
			if limitMode == "historical_board" {
				if d < "2022-04-29" {
					transfer = .00002
				}
				if d < "2023-08-28" {
					stamp = .001
				}
			}
			in.Bars[d][code] = Bar{Open: o, High: h, Low: l, Close: close, MA10: ma, Assumed: true, Observation: observation, Buy: usable && index >= 9 && lower < o && o < upper, Sell: usable && lower < o && o < upper, Intraday: usable && !anomalous && h > l && h > lower, Lower: lower, Upper: upper, Commission: .0003, Minimum: 5, Transfer: transfer, Stamp: stamp, ADV: adv}
		}
	}
	return in, audit, in.Validate(ctx)
}
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
