package fourlightstudy

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestHorizonsPreservePriorReportsAndEvents(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	var before, after []Event
	b, s, tr, ep, err := AnalyzePriceEpisodeStudy(context.Background(), m, source, sectors, func(e Event) error { before = append(before, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	gb, gs, gt, ge, h, err := AnalyzeWaveHorizons(context.Background(), m, source, sectors, func(e Event) error { after = append(after, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b, gb) || !reflect.DeepEqual(s, gs) || !reflect.DeepEqual(tr, gt) || !reflect.DeepEqual(ep, ge) {
		t.Fatal("changed short reports")
	}
	if h.Status != "complete" || len(before) != len(after) {
		t.Fatal("incomplete report")
	}
	for i, e := range after {
		if e.Horizons == nil || !reflect.DeepEqual(e.Wave.Windows[0], e.Horizons.StartupFive) {
			t.Fatal("changed startup")
		}
		e.Horizons = nil
		if !reflect.DeepEqual(e, before[i]) {
			t.Fatal("old event changed")
		}
	}
	assertHorizonPartitions(t, h.Groups)
	assertHorizonPartitions(t, h.Daily)
	assertHorizonPartitions(t, h.Monthly)
	found := false
	for _, row := range h.Groups {
		if row.StartupFive == "all" && row.Name == "first_all_red" {
			if row.Selected != groupWindow(t, b, "first_all_red", 5).Selected {
				t.Fatal("long future discarded signals")
			}
			if row.Days == 20 && row.Known > 0 {
				found = true
			}
			if row.Days >= 40 && (row.Known != 0 || row.MeanEndNetPct != nil) {
				t.Fatal("partial prefix entered long mean")
			}
		}
	}
	if !found {
		t.Fatal("fixture did not cover a complete long window")
	}
}
func assertHorizonPartitions(t *testing.T, rows []HorizonStats) {
	t.Helper()
	by := map[horizonKey]HorizonStats{}
	for _, r := range rows {
		if r.Selected != r.Entered+r.NotEntered+r.EntryUnknown || r.Entered != r.Known+r.Unknown {
			t.Fatal("invalid funnel")
		}
		by[horizonKey{r.Date + r.Month, r.Name, r.StartupFive, r.Days}] = r
	}
	counts := func(r HorizonStats) [6]int {
		return [6]int{r.Selected, r.Entered, r.NotEntered, r.EntryUnknown, r.Known, r.Unknown}
	}
	for key, r := range by {
		if key.startup != "all" {
			continue
		}
		sum := [6]int{}
		for _, state := range horizonStartupStates[1:] {
			for i, n := range counts(by[horizonKey{key.period, key.name, state, key.days}]) {
				sum[i] += n
			}
		}
		if sum != counts(r) {
			t.Fatal("startup partition lost unknown")
		}
		if key.name == "first_all_red" {
			sum = [6]int{}
			for _, name := range episodePartition {
				for i, n := range counts(by[horizonKey{key.period, name, "all", key.days}]) {
					sum[i] += n
				}
			}
			if sum != counts(r) {
				t.Fatal("episode partition lost long coverage")
			}
		}
	}
}
func TestHorizonMatchingExcludesSelfAndWeightsDates(t *testing.T) {
	grid, daily := horizonMatchGrid{}, horizonGrid{}
	for _, date := range []string{"2026-01-01", "2026-01-02"} {
		values := []float64{10, 0, 0, 0, 0, 0}
		if date == "2026-01-02" {
			values = []float64{30, 12, 12, 12, 12, 12}
		}
		for i, v := range values {
			h := &strategy.WaveHorizons{Model: strategy.WaveHorizonsModel, EntryStatus: "entered", StartupFive: strategy.UpwardWaveWindow{Status: "success"}}
			for _, n := range horizonLengths {
				zero := 0
				f := 0.0
				h.Windows = append(h.Windows, strategy.WaveHorizonWindow{Days: n, Status: "known", Full: &strategy.UpwardWaveMetrics{EndNetPct: v}, PeakCloseNetPct: &v, GivebackPct: &f, AfterDay5CloseReturnPct: &v, AfterDay5UpRatio: &f, AfterDay5LongestUp: &zero, NewClosingHighDays: &zero})
			}
			e := Event{Date: date, Context: &SectorSignalContext{SectorCode: "A"}, Horizons: h}
			names := []string{"baseline"}
			if i == 0 {
				names = append(names, "first_all_red")
			}
			daily.add(date, date, names, h)
			grid.add(e, names)
		}
	}
	got := grid.compare([]string{"2026-01-01", "2026-01-02", "2026-01-03"}, "", daily)
	found := false
	for _, r := range got {
		if r.Group == "first_all_red" && r.Reference == "matched_sector" && r.Metric == "end_net_pct" && r.Days == 20 {
			found = true
			if r.Selected != 2 || r.CandidateKnown != 2 || r.MatchedKnown != 2 || r.PairedDates != 2 || r.UnpairedDates != 1 || math.Abs(*r.MeanDifference-14) > 1e-12 {
				t.Fatalf("self exclusion or date mean incorrect: %+v", r)
			}
		}
	}
	if !found {
		t.Fatal("missing comparison")
	}
	// Too few other known stocks must leave the candidate unmatched.
	grid[horizonMatchKey{"2026-01-01", "A", "baseline", 20}].known = 5
	for _, r := range grid.compare([]string{"2026-01-01"}, "", daily) {
		if r.Group == "first_all_red" && r.Reference == "matched_sector" && r.Days == 20 {
			if r.MatchedKnown != 0 || r.MeanDifference != nil {
				t.Fatal("insufficient peers accepted")
			}
		}
	}
}
func TestHorizonsErrorsDoNotReturnPartialReports(t *testing.T) {
	for _, mode := range []string{"source", "sink", "cancel", "legacy", "classification"} {
		t.Run(mode, func(t *testing.T) {
			m, source, sectors := sectorStudyFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sink func(Event) error
			switch mode {
			case "source":
				source.loadErr = errors.New("source failure")
			case "sink":
				sink = func(Event) error { return errors.New("sink failure") }
			case "cancel":
				cancel()
			case "legacy":
				m.Version = 1
			case "classification":
				sectors = SectorSnapshot{}
			}
			b, s, tr, e, h, err := AnalyzeWaveHorizons(ctx, m, source, sectors, sink)
			if err == nil || s != nil || tr != nil || e != nil || h != nil || !reflect.DeepEqual(b, Report{}) {
				t.Fatal("partial result on failure")
			}
		})
	}
}

func TestHorizonIndustryMatchingUsesCandidateCompositionThenEqualDates(t *testing.T) {
	grid, daily := horizonMatchGrid{}, horizonGrid{}
	add := func(date, sector string, values []float64, candidates int) {
		for i, v := range values {
			zero, count := 0.0, 0
			h := &strategy.WaveHorizons{EntryStatus: "entered", StartupFive: strategy.UpwardWaveWindow{Status: "success"}, Windows: []strategy.WaveHorizonWindow{{
				Days: 20, Status: "known", Full: &strategy.UpwardWaveMetrics{EndNetPct: v}, PeakCloseNetPct: &v, GivebackPct: &zero,
				AfterDay5CloseReturnPct: &v, AfterDay5UpRatio: &zero, AfterDay5LongestUp: &count, NewClosingHighDays: &count,
			}}}
			names := []string{"baseline"}
			if i < candidates {
				names = append(names, "first_all_red")
			}
			daily.add(date, date, names, h)
			grid.add(Event{Date: date, Context: &SectorSignalContext{SectorCode: sector}, Horizons: h}, names)
		}
	}
	// First date: A's two candidates have deltas 24 and 12, B's one has
	// delta 6. Candidate composition yields 14, not sector-equal 12.
	add("2026-01-01", "A", []float64{30, 20, 10, 0, 0, 0}, 2)
	add("2026-01-01", "B", []float64{6, 0, 0, 0, 0, 0}, 1)
	// An extreme candidate with only four peers must not enter the mean.
	add("2026-01-01", "C", []float64{999, 0, 0, 0, 0}, 1)
	// Second date: one candidate with delta -2; date equality yields 6,
	// not the event-weighted 10 across the four matched candidates.
	add("2026-01-02", "A", []float64{0, 2, 2, 2, 2, 2}, 1)
	for _, row := range grid.compare([]string{"2026-01-01", "2026-01-02"}, "", daily) {
		if row.Group == "first_all_red" && row.Reference == "matched_sector" && row.Metric == "end_net_pct" && row.Days == 20 {
			if row.Selected != 5 || row.CandidateKnown != 5 || row.MatchedKnown != 4 || row.UnmatchedKnown != 1 || row.PairedDates != 2 || row.MeanDifference == nil || math.Abs(*row.MeanDifference-6) > 1e-12 {
				t.Fatalf("incorrect industry composition or date weighting: %+v", row)
			}
			return
		}
	}
	t.Fatal("missing industry comparison")
}

func TestHorizonNamesIncludeSignalTimeRelativeGroups(t *testing.T) {
	h := &strategy.WaveHorizons{EntryStatus: "entered", StartupFive: strategy.UpwardWaveWindow{Status: "success"}}
	e := Event{
		Groups:   []string{"first_all_red", moderateVolumeGroup},
		Episode:  &PriceEpisodeContext{Group: "first"},
		Context:  &SectorSignalContext{Policy: SectorContextPolicy, StockStage: "outperform", SectorStage: "not_outperform"},
		Horizons: h,
	}
	names, err := horizonNamesForEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"baseline", "first_all_red", "episode_first", moderateVolumeGroup, stockOutperformSector, sectorNotOutperformMarket} {
		if !hasSectorGroup(names, want) {
			t.Fatalf("missing context group %q in %v", want, names)
		}
	}
	for _, absent := range []string{stockNotOutperformSector, sectorOutperformMarket, contextUnknown} {
		if hasSectorGroup(names, absent) {
			t.Fatalf("unexpected context group %q in %v", absent, names)
		}
	}
}

func TestHorizonContextCountsMatchSignalTimeSectorReport(t *testing.T) {
	for _, mode := range []string{"published", "current_only", "not_yet_public", "missing_member", "missing_future"} {
		t.Run(mode, func(t *testing.T) {
			m, source, sectors := sectorStudyFixture(t)
			m.Through = m.From
			_, originalSector, err := AnalyzeSectorContext(context.Background(), m, source, sectors, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "current_only":
				sectors.Basis, sectors.PublishedOn = "current_only", ""
			case "not_yet_public":
				sectors.PublishedOn = m.From
			case "missing_member":
				sectors.Memberships = sectors.Memberships[1:]
			case "missing_future":
				for code, pair := range source.pairs {
					end := 0
					for end < len(pair.Adjusted) && pair.Adjusted[end].Date <= m.From {
						end++
					}
					pair.Adjusted, pair.Raw = pair.Adjusted[:end], pair.Raw[:end]
					pair.AdjustedShares, pair.RawShares = pair.AdjustedShares[:end], pair.RawShares[:end]
					source.pairs[code] = pair
				}
			}
			_, sector, _, _, horizons, err := AnalyzeWaveHorizons(context.Background(), m, source, sectors, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range horizonContextGroups {
				want := groupWindow(t, Report{Groups: sector.Groups}, name, 5).Selected
				if mode == "missing_future" && want != groupWindow(t, Report{Groups: originalSector.Groups}, name, 5).Selected {
					t.Fatalf("future missingness changed signal-time group %s", name)
				}
				found := 0
				for _, row := range horizons.Groups {
					if row.Name != name || row.StartupFive != "all" {
						continue
					}
					found++
					if row.Selected != want {
						t.Fatalf("%s D%d: selected=%d, signal-time count=%d", name, row.Days, row.Selected, want)
					}
					if mode == "missing_future" && (row.Known != 0 || row.EntryUnknown != row.Selected) {
						t.Fatalf("future unknown removed or fabricated outcomes: %+v", row)
					}
				}
				if found != 3 {
					t.Fatalf("missing long windows for %s", name)
				}
			}
			first := groupWindow(t, Report{Groups: sector.Groups}, "first_all_red", 5).Selected
			if first != 12 {
				t.Fatal("fixture must contain twelve first triggers")
			}
			if mode == "published" || mode == "missing_future" {
				for _, name := range horizonContextGroups[:4] {
					if groupWindow(t, Report{Groups: sector.Groups}, name, 5).Selected == 0 {
						t.Fatalf("fixture must exercise both relative states: %s", name)
					}
				}
			}
			if mode == "current_only" || mode == "not_yet_public" {
				if groupWindow(t, Report{Groups: sector.Groups}, contextUnknown, 5).Selected != first {
					t.Fatal("unavailable historical classification escaped unknown group")
				}
			}
		})
	}
}
