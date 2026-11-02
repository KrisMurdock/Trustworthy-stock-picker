package strategy

import (
	"math"
	"reflect"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func wavePairFixture() []market.Bar {
	return []market.Bar{
		{Date: "2026-05-19", Open: 9.1, Close: 9.8, High: 10, Low: 9, Volume: 100000},
		{Date: "2026-05-20", Open: 10, Close: 11, High: 11.5, Low: 9.8, Volume: 120000},
		{Date: "2026-05-21", Open: 10.8, Close: 10.3, High: 11.1, Low: 10.1, Volume: 90000},
		{Date: "2026-05-22", Open: 10.4, Close: 12, High: 12.2, Low: 10.2, Volume: 150000},
		{Date: "2026-05-25", Open: 12.1, Close: 12.7, High: 13.3, Low: 11.9, Volume: 170000},
	}
}

func wavePairTransform(raw []market.Bar, scale, offset float64, round bool) []market.Bar {
	adjusted := append([]market.Bar(nil), raw...)
	for i := range adjusted {
		for _, p := range []*float64{&adjusted[i].Open, &adjusted[i].Close, &adjusted[i].High, &adjusted[i].Low} {
			*p = scale**p + offset
			if round {
				*p = math.Round(*p*100) / 100
			}
		}
	}
	return adjusted
}

func TestWavePricePairStableMappings(t *testing.T) {
	for _, tc := range []struct {
		name, model   string
		scale, offset float64
		round         bool
	}{
		{"identity", "translation", 1, 0, false},
		{"cash_offset", "translation", 1, -.50, false},
		{"proportional", "proportional", .7, 0, false},
		{"affine", "affine", .8, -.5, false},
		{"proportional_rounded", "proportional", .713, 0, true},
		{"affine_rounded", "affine", .813, -.536, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := wavePairFixture()
			adjusted := wavePairTransform(raw, tc.scale, tc.offset, tc.round)
			r, reason := NewWavePricePairReference(adjusted, raw)
			if reason != "" || r.Policy != WavePricePairPolicy || r.Model != tc.model || r.EvidenceSessions != 5 || r.EvidenceFrom != raw[0].Date || r.EvidenceThrough != raw[4].Date {
				t.Fatalf("reference: %+v, %s", r, reason)
			}
			future := market.Bar{Date: "2026-05-26", Open: 13.7, Close: 14, High: 14.2, Low: 13.5, Volume: 200000}
			a := wavePairTransform([]market.Bar{future}, tc.scale, tc.offset, tc.round)[0]
			if reason := r.Validate(a, future); reason != "" {
				t.Fatalf("stable future transform rejected: %s, %+v", reason, r)
			}
		})
	}
}

func TestWavePricePairExactRatioWinsOverApproximateTranslation(t *testing.T) {
	raw := wavePairFixture()
	for i := range raw {
		price := 10 + .075*float64(i)
		raw[i].Open, raw[i].Close, raw[i].High, raw[i].Low = price, price+.05, price+.1, price
	}
	adjusted := wavePairTransform(raw, .98, 0, false)
	r, reason := NewWavePricePairReference(adjusted, raw)
	// A translation of -0.204 fits this narrow prefix within 0.004 yuan, but
	// the exact proportional mapping has smaller evidence residuals. Picking
	// the first tolerated candidate would reject a later legitimate rally.
	if reason != "" || r.Model != "proportional" || math.Abs(r.Scale-.98) > 1e-12 || r.Offset != 0 {
		t.Fatalf("exact proportional mapping was obscured: %+v, %s", r, reason)
	}
	future := market.Bar{Date: "2026-05-26", Open: 12, Close: 12.1, High: 12.2, Low: 11.9, Volume: 100000}
	a := wavePairTransform([]market.Bar{future}, .98, 0, false)[0]
	if got := r.Validate(a, future); got != "" {
		t.Fatal("unchanged proportional mapping rejected after price movement", got)
	}
}

func TestWavePricePairNearEqualResidualPrefersSimplerModel(t *testing.T) {
	raw := wavePairFixture()
	adjusted := wavePairTransform(raw, .9999999, 0, false)
	r, reason := NewWavePricePairReference(adjusted, raw)
	if reason != "" || r.Model != "translation" || r.MaximumEvidenceResidual > wavePairEpsilon {
		t.Fatalf("sub-micro-yuan improvement changed model: %+v, %s", r, reason)
	}
}

func TestWavePricePairRealAdditiveMappingDoesNotUseCloseRatio(t *testing.T) {
	// sh601566, 2026-05-19 through 2026-05-25, frozen development data.
	// All four fields differ by exactly 0.50 yuan. On 2026-05-28 the close
	// ratio drifts by -0.65%, but the complete OHLC transformation is stable.
	raw := []market.Bar{
		{Date: "2026-05-19", Open: 11.44, Close: 11.39, High: 11.74, Low: 11.23, Volume: 17507600},
		{Date: "2026-05-20", Open: 11.39, Close: 11.98, High: 12, Low: 11.28, Volume: 31791200},
		{Date: "2026-05-21", Open: 11.98, Close: 12.05, High: 12.19, Low: 11.71, Volume: 29252700},
		{Date: "2026-05-22", Open: 12.05, Close: 12.12, High: 12.18, Low: 11.8, Volume: 22154400},
		{Date: "2026-05-25", Open: 12.13, Close: 12.7, High: 13.33, Low: 11.93, Volume: 38803800},
	}
	adjusted := wavePairTransform(raw, 1, -.5, false)
	r, reason := NewWavePricePairReference(adjusted, raw)
	if reason != "" || r.Model != "translation" || math.Abs(r.Offset+.5) > 1e-12 {
		t.Fatal(r, reason)
	}
	future := market.Bar{Date: "2026-05-28", Open: 11.68, Close: 10.96, High: 11.85, Low: 10.93, Volume: 100000}
	a := wavePairTransform([]market.Bar{future}, 1, -.5, false)[0]
	if math.Abs((a.Close/future.Close)/(adjusted[4].Close/raw[4].Close)-1) <= .002 {
		t.Fatal("fixture no longer reproduces the obsolete ratio conflict")
	}
	if reason := r.Validate(a, future); reason != "" {
		t.Fatal("stable additive mapping rejected", reason)
	}
}

func TestWavePricePairFrozenReferenceRejectsChangedOHLCAndBadPairs(t *testing.T) {
	raw := wavePairFixture()
	adjusted := wavePairTransform(raw, .8, -.5, false)
	r, reason := NewWavePricePairReference(adjusted, raw)
	if reason != "" {
		t.Fatal(reason)
	}
	future := market.Bar{Date: "2026-05-26", Open: 13, Close: 13.2, High: 13.5, Low: 12.9, Volume: 100000}
	a := wavePairTransform([]market.Bar{future}, .8, -.5, false)[0]
	for _, tc := range []struct {
		name, want string
		change     func(*market.Bar, *market.Bar)
	}{
		{"offset_change", "price_transform_changed", func(a, b *market.Bar) { *a = wavePairTransform([]market.Bar{*b}, .8, -.7, false)[0] }},
		{"scale_change", "price_transform_changed", func(a, b *market.Bar) { *a = wavePairTransform([]market.Bar{*b}, .7, -.5, false)[0] }},
		{"high_only_change", "price_transform_changed", func(a, _ *market.Bar) { a.High += .05 }},
		{"volume_absolute_limit", "pairing_volume_mismatch", func(a, _ *market.Bar) { a.Volume += 101 }},
		{"volume_wrong_unit", "pairing_volume_mismatch", func(a, _ *market.Bar) { a.Volume /= 100 }},
		{"date_mismatch", "pairing_date_mismatch", func(a, _ *market.Bar) { a.Date = "2026-05-27" }},
		{"bad_date", "pairing_date_mismatch", func(a, b *market.Bar) { a.Date, b.Date = "2026-02-30", "2026-02-30" }},
		{"nan_price", "pairing_bar_invalid", func(a, _ *market.Bar) { a.High = math.NaN() }},
		{"invalid_ohlc", "pairing_bar_invalid", func(a, _ *market.Bar) { a.High = a.Low - .1 }},
		{"negative_price", "pairing_bar_invalid", func(a, _ *market.Bar) { a.Low = -1 }},
		{"nonfinite_volume", "pairing_bar_invalid", func(a, _ *market.Bar) { a.Volume = math.Inf(1) }},
		{"suspended", "pairing_bar_invalid", func(a, b *market.Bar) { a.Volume, b.Volume = 0, 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ac, rc := a, future
			tc.change(&ac, &rc)
			if got := r.Validate(ac, rc); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	a.Volume += 100
	if got := r.Validate(a, future); got != "" {
		t.Fatal("one-lot rounding difference should be tolerated", got)
	}
	if got := (WavePricePairReference{}).Validate(a, future); got != "pairing_reference_missing" {
		t.Fatal("zero reference accepted", got)
	}
}

func TestWavePricePairEvidenceIsCausalBoundedAndDetached(t *testing.T) {
	raw := wavePairFixture()
	adjusted := wavePairTransform(raw, 1, -.5, false)
	initial, reason := NewWavePricePairReference(adjusted, raw)
	if reason != "" {
		t.Fatal(reason)
	}
	// Unequal history lengths are allowed; only the recent date-aligned suffix
	// establishes the reference. Older malformed prices must not be read.
	older := market.Bar{Date: "2026-05-18", Open: math.NaN()}
	longRaw := append([]market.Bar{older}, raw...)
	longAdjusted := append([]market.Bar{older}, adjusted...)
	for _, pair := range [][2][]market.Bar{{adjusted, longRaw}, {longAdjusted, longRaw}} {
		got, reason := NewWavePricePairReference(pair[0], pair[1])
		if reason != "" || !reflect.DeepEqual(initial, got) {
			t.Fatal("older/unequal prefix changed bounded reference", got, reason)
		}
	}
	future := market.Bar{Date: "2026-05-26", Open: 13, Close: 13.2, High: 13.5, Low: 12.9, Volume: 100000}
	a := wavePairTransform([]market.Bar{future}, 1, -.5, false)[0]
	for i := range raw {
		raw[i].Close = math.NaN()
		adjusted[i].High = 1e9
	}
	if got := initial.Validate(a, future); got != "" {
		t.Fatal("reference retained mutable input slices", got)
	}
}

func TestWavePricePairInsufficientOrConflictingPrefix(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*[]market.Bar, *[]market.Bar)
	}{
		{"short", "pairing_evidence_missing", func(a, r *[]market.Bar) { *a, *r = (*a)[:2], (*r)[:2] }},
		{"missing_day", "pairing_date_mismatch", func(a, _ *[]market.Bar) { *a = append((*a)[:2], (*a)[3:]...) }},
		{"duplicate_day", "pairing_date_mismatch", func(a, r *[]market.Bar) { (*a)[2].Date, (*r)[2].Date = (*a)[1].Date, (*r)[1].Date }},
		{"volume", "pairing_volume_mismatch", func(a, _ *[]market.Bar) { (*a)[2].Volume += 101 }},
		{"invalid_price", "pairing_bar_invalid", func(a, _ *[]market.Bar) { (*a)[2].Low = math.Inf(-1) }},
		{"recent_transform_change", "pairing_transform_inconsistent", func(a, r *[]market.Bar) { (*a)[4] = wavePairTransform((*r)[4:], 1, -.9, false)[0] }},
		{"flat", "pairing_insufficient_price_range", func(a, r *[]market.Bar) {
			for i := range *a {
				date := (*a)[i].Date
				(*r)[i] = market.Bar{Date: date, Open: 10, Close: 10, High: 10, Low: 10, Volume: 100000}
				(*a)[i] = market.Bar{Date: date, Open: 9.5, Close: 9.5, High: 9.5, Low: 9.5, Volume: 100000}
			}
		}},
		{"narrow", "pairing_insufficient_price_range", func(a, r *[]market.Bar) {
			for i := range *a {
				date := (*a)[i].Date
				(*r)[i] = market.Bar{Date: date, Open: 10, Close: 10.05, High: 10.1, Low: 10, Volume: 100000}
			}
			*a = wavePairTransform(*r, 1, -.5, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := wavePairFixture()
			adjusted := wavePairTransform(raw, 1, -.5, false)
			tc.change(&adjusted, &raw)
			if _, reason := NewWavePricePairReference(adjusted, raw); reason != tc.want {
				t.Fatalf("got %q, want %q", reason, tc.want)
			}
		})
	}
	// The documented minimum of three sessions is sufficient with price range.
	raw := wavePairFixture()[:3]
	if _, reason := NewWavePricePairReference(wavePairTransform(raw, 1, -.5, false), raw); reason != "" {
		t.Fatal("three valid sessions rejected", reason)
	}
}
