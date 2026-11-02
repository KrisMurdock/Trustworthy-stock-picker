package fourlightstudy

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func enableV2(m *Manifest) {
	m.Version, m.WaveModel, m.PairingPolicy = 2, strategy.UpwardWaveNoDrawdownModel, PairingPolicy
}

func studyV2Fixture(t *testing.T) (Manifest, *testSource) {
	m, source := studyFixture(t)
	enableV2(&m)
	for code, pair := range source.pairs {
		for _, bar := range pair.Adjusted {
			pair.AdjustedShares = append(pair.AdjustedShares, bar.Volume*100)
		}
		for _, bar := range pair.Raw {
			pair.RawShares = append(pair.RawShares, bar.Volume*100)
		}
		pair.AdjustedSource, pair.RawSource = "tencent", "sina"
		source.pairs[code] = pair
	}
	return m, source
}

func TestManifestExplicitModelCompatibility(t *testing.T) {
	m, _ := studyFixture(t)
	if m.Validate() != nil {
		t.Fatal("legacy manifest rejected")
	}
	m.WaveModel = strategy.UpwardWaveNoDrawdownModel
	if m.Validate() == nil {
		t.Fatal("legacy manifest accepted new policy")
	}
	enableV2(&m)
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Manifest){
		func(m *Manifest) { m.WaveModel = strategy.UpwardWaveModel },
		func(m *Manifest) { m.PairingPolicy = "" },
		func(m *Manifest) { m.Version = 3 },
	} {
		bad := m
		edit(&bad)
		if bad.Validate() == nil {
			t.Fatalf("bad manifest accepted: %+v", bad)
		}
	}
}

func TestModerateVolumeUsesOnlySignalDayAndExactBounds(t *testing.T) {
	for _, tc := range []struct {
		ratio float64
		want  string
	}{
		{1.199999, ""}, {1.2, moderateVolumeGroup}, {1.999999, moderateVolumeGroup}, {2, highVolumeGroup},
	} {
		bars := make([]market.Bar, 6)
		for i := range bars {
			bars[i].Volume = 100
		}
		bars[5].Volume = tc.ratio * 100
		groups := classifyVolume([]string{"baseline", "first_all_red"}, signalVolumeRatio(bars))
		if tc.want == "" && len(groups) != 2 || tc.want != "" && (len(groups) != 3 || groups[2] != tc.want) {
			t.Fatal(tc, groups)
		}
		if got := classifyVolume([]string{"continuing_all_red"}, signalVolumeRatio(bars)); len(got) != 1 {
			t.Fatal("candidate admitted continuing all-red")
		}
	}
}

func TestV2GroupsRetainUnknownAndBoardDenominators(t *testing.T) {
	m, source := studyV2Fixture(t)
	pair := source.pairs["sz000001"]
	// Two first-all-red signals: one at volume ratio 2, one below 2.
	source.stocks = append(source.stocks, config.Stock{Code: "sh688001", Name: "STAR"})
	source.pairs["sh688001"] = Pair{Adjusted: pair.Adjusted, AdjustedShares: pair.AdjustedShares, AdjustedSource: "tencent"}
	events := []Event{}
	r, err := Analyze(context.Background(), m, source, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	first, moderate, high := groupWindow(t, r, "first_all_red", 5), groupWindow(t, r, moderateVolumeGroup, 5), groupWindow(t, r, highVolumeGroup, 5)
	if first.Selected != 4 || moderate.Selected != 2 || high.Selected != 2 || first.EntryUnknown != 2 || moderate.EntryUnknown != 1 || high.EntryUnknown != 1 {
		t.Fatalf("denominators first=%+v moderate=%+v high=%+v", first, moderate, high)
	}
	if r.Version != 2 || r.Engine != EngineV2 || len(r.CandidateRetention) != 4 || len(r.CandidateComparisons) != 4 || len(r.PairSources) != 2 {
		t.Fatalf("missing v2 output: %+v", r)
	}
	for _, row := range r.Groups {
		if row.Wave.Model != strategy.UpwardWaveNoDrawdownModel {
			t.Fatal("model mixed")
		}
	}
	selected := 0
	for _, row := range r.Boards {
		if row.Name == "first_all_red" {
			selected += row.Wave.Windows[0].Selected
		}
	}
	if selected != first.Selected {
		t.Fatal("board denominator mismatch")
	}
	for _, e := range events {
		if e.Board == "" || e.VolumeRatio == nil {
			t.Fatal("missing event provenance")
		}
	}
	before := events
	for code, pair := range source.pairs {
		pair.Adjusted = append([]market.Bar(nil), pair.Adjusted...)
		for i := 84; i < len(pair.Adjusted); i++ {
			pair.Adjusted[i].Close *= 2
		}
		source.pairs[code] = pair
	}
	events = nil
	if _, err := Analyze(context.Background(), m, source, func(e Event) error { events = append(events, e); return nil }); err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		if !reflect.DeepEqual(e.Groups, before[i].Groups) || !reflect.DeepEqual(e.FourLight, before[i].FourLight) || !reflect.DeepEqual(e.VolumeRatio, before[i].VolumeRatio) {
			t.Fatal("future path changed candidate signal")
		}
	}
}

func TestNormalizedPairPreservesSignalVolumeAndRequiresEvidence(t *testing.T) {
	pair := Pair{Adjusted: []market.Bar{{Volume: 12.000000001}}, Raw: []market.Bar{{Volume: 1200}}, AdjustedShares: []float64{1200}, RawShares: []float64{1200}}
	a, r, err := normalizedPair(pair)
	if err != nil || a[0].Volume != 1200 || r[0].Volume != 1200 || pair.Adjusted[0].Volume != 12.000000001 {
		t.Fatal("normalization mutated signal or failed")
	}
	pair.RawShares = nil
	if _, _, err := normalizedPair(pair); err == nil {
		t.Fatal("accepted missing normalization evidence")
	}
}

func TestBadShareMetadataDoesNotRemoveFourLightSignals(t *testing.T) {
	m, source := studyV2Fixture(t)
	m.Through = m.From
	before, err := Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	pair := source.pairs["sz000001"]
	pair.AdjustedShares[80] = math.NaN()
	source.pairs["sz000001"] = pair
	after, err := Analyze(context.Background(), m, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, a := groupWindow(t, before, "first_all_red", 5), groupWindow(t, after, "first_all_red", 5)
	if a.Selected != b.Selected || a.EntryUnknown != 1 || a.Known != 0 || before.Audit.StateKnown != after.Audit.StateKnown {
		t.Fatalf("share defect removed signal: before=%+v after=%+v", b, a)
	}
}

func TestCandidateComparisonUsesUnfilteredFirstOnSameDates(t *testing.T) {
	row := func(date, name string, known, success int) GroupStats {
		return GroupStats{Date: date, Name: name, Wave: &strategy.UpwardWaveSummary{Windows: []strategy.UpwardWaveStats{{Days: 5, Known: known, Success: success}}}}
	}
	dates := []string{"2026-04-08", "2026-04-09", "2026-04-10"}
	rows := []GroupStats{row(dates[0], "first_all_red", 10, 2), row(dates[0], moderateVolumeGroup, 4, 2), row(dates[1], "first_all_red", 100, 50), row(dates[1], moderateVolumeGroup, 1, 0), row(dates[2], "first_all_red", 10, 2)}
	comparisons, err := compareNamedGroups(context.Background(), dates, rows, []string{moderateVolumeGroup}, "first_all_red")
	if err != nil {
		t.Fatal(err)
	}
	x := comparisons[0]
	if x.Reference != "first_all_red" || x.PairedDates != 2 || x.UnpairedDates != 1 || x.MeanDifference == nil || *x.MeanDifference != -.1 {
		t.Fatalf("wrong comparator or weights %+v", x)
	}
}
