package fourlightstudy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func sectorStudyFixture(t *testing.T) (Manifest, *testSource, SectorSnapshot) {
	t.Helper()
	m, original := studyV2Fixture(t)
	template := original.pairs["sz000001"]
	source := &testSource{pairs: map[string]Pair{}}
	sectors := SectorSnapshot{Version: 1, Basis: "published_snapshot", Taxonomy: "test industries", Level: "industry",
		SnapshotAsOf: "2025-09-30", PublishedOn: "2025-10-02", ObservedAt: "2026-11-02T09:00:00+08:00",
		Sources: []SectorSource{{URL: "https://example.org/frozen-classification.pdf", SHA256: strings.Repeat("b", 64)}}}
	for n := range 12 {
		code, sector := fmt.Sprintf("sz%06d", n+1), "A"
		slope := .2 + float64(n)*.01
		if n >= 6 {
			sector, slope = "B", .05+float64(n-6)*.002
		}
		pair := template
		pair.Adjusted = append([]market.Bar(nil), template.Adjusted...)
		for i := range pair.Adjusted {
			price := 100 + slope*float64(i)
			pair.Adjusted[i].Open, pair.Adjusted[i].High = price-.05, price+.1
			pair.Adjusted[i].Low, pair.Adjusted[i].Close = price-.1, price
		}
		pair.Raw = append([]market.Bar(nil), pair.Adjusted...)
		pair.AdjustedShares = append([]float64(nil), template.AdjustedShares...)
		pair.RawShares = append([]float64(nil), template.RawShares...)
		source.pairs[code] = pair
		source.stocks = append(source.stocks, config.Stock{Code: code, Name: code, ListedFrom: m.Calendar.Days[0]})
		sectors.Memberships = append(sectors.Memberships, SectorMembership{Code: code, SectorCode: sector, SectorName: "Industry " + sector})
	}
	return m, source, sectors
}

func TestSectorContextPreservesBaseReportAndEmitsOnlyAllRed(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	var oldEvents, newEvents []Event
	base, err := Analyze(context.Background(), m, source, func(e Event) error { oldEvents = append(oldEvents, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	source.loads = 0
	got, report, err := AnalyzeSectorContext(context.Background(), m, source, sectors, func(e Event) error { newEvents = append(newEvents, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, base) || source.loads != 2*len(source.stocks) {
		t.Fatalf("base evaluation changed or passes were not streaming: loads=%d", source.loads)
	}
	if report.Status != "complete" || !report.HistoricalEligible || report.EligibleSignalDates != 4 || report.SectorSnapshot.Memberships != 12 || report.Audit.StockDates != base.Audit.StateKnown {
		t.Fatalf("incomplete metadata or audit: %+v", report.Audit)
	}
	if len(newEvents) != len(oldEvents) || len(newEvents) != base.Audit.AllRedEventRows {
		t.Fatalf("event emission changed: %d vs %d", len(newEvents), len(oldEvents))
	}
	for i, event := range newEvents {
		c := event.Context
		if !event.FourLight.AllRed || c == nil || !c.HistoricalEligible || c.SectorOtherStocks != 5 || c.MarketOtherStocks != 11 {
			t.Fatalf("missing context or incorrect self exclusion: %+v", event)
		}
		if event.Code <= "sz000006" && c.SectorStage != "outperform" || event.Code > "sz000006" && c.SectorStage != "not_outperform" {
			t.Fatalf("industry strength inconsistent: code=%s context=%+v", event.Code, c)
		}
		event.Context = nil
		if !reflect.DeepEqual(event, oldEvents[i]) {
			t.Fatal("context changed original event")
		}
	}
	for _, name := range sectorOutputGroups {
		for _, days := range []int{5, 10} {
			original := groupWindow(t, base, name, days)
			contextStats := groupWindow(t, Report{Groups: report.Groups}, name, days)
			if !reflect.DeepEqual(original, contextStats) {
				t.Fatalf("context report changed %s denominator", name)
			}
			selected, known := 0, 0
			for _, row := range report.Sectors {
				if row.Name == name {
					for _, w := range row.Wave.Windows {
						if w.Days == days {
							selected += w.Selected
							known += w.Known
						}
					}
				}
			}
			if selected != original.Selected || known != original.Known {
				t.Fatalf("industry summaries dropped observations: %s %d/%d", name, selected, known)
			}
		}
	}
	first := groupWindow(t, base, "first_all_red", 5).Selected
	groupSelected := func(name string) int { return groupWindow(t, Report{Groups: report.Groups}, name, 5).Selected }
	if groupSelected(stockOutperformSector)+groupSelected(stockNotOutperformSector) != first || groupSelected(sectorOutperformMarket)+groupSelected(sectorNotOutperformMarket) != first || groupSelected(contextUnknown) != 0 {
		t.Fatal("independent stage partitions lost first signals")
	}
	if len(report.Daily) != 4*len(sectorContextGroups) || len(report.SectorDaily) != 4*2*len(sectorOutputGroups) || len(report.MonthlyComparisons) != 1 || len(report.CandidateRetention) != 12 {
		t.Fatal("missing daily, monthly or retention evidence")
	}
}

func TestSectorContextPriorsIgnoreFutureOutcomeAvailability(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	m.Through = m.From
	var before, after []Event
	_, previous, err := AnalyzeSectorContext(context.Background(), m, source, sectors, func(e Event) error { before = append(before, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for code, pair := range source.pairs {
		// The entire future is absent for one industry. These stocks must still
		// supply signal-time returns, despite all their wave windows being unknown.
		if code <= "sz000006" {
			pair.Adjusted, pair.Raw = pair.Adjusted[:81], pair.Raw[:81]
			pair.AdjustedShares, pair.RawShares = pair.AdjustedShares[:81], pair.RawShares[:81]
		} else {
			for i := 81; i < len(pair.Adjusted); i++ {
				price := 50 - float64(i-81)*.1
				pair.Adjusted[i].Open, pair.Adjusted[i].High = price, price+.1
				pair.Adjusted[i].Low, pair.Adjusted[i].Close = price-.1, price
				pair.Raw[i] = pair.Adjusted[i]
			}
		}
		source.pairs[code] = pair
	}
	_, current, err := AnalyzeSectorContext(context.Background(), m, source, sectors, func(e Event) error { after = append(after, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || len(after) != 12 {
		t.Fatal("future observations changed signal count")
	}
	for i := range before {
		if !reflect.DeepEqual(before[i].Context, after[i].Context) || !reflect.DeepEqual(before[i].Groups, after[i].Groups) || !reflect.DeepEqual(before[i].FourLight, after[i].FourLight) {
			t.Fatalf("future outcome leaked into prior/selection: %s", after[i].Code)
		}
	}
	for _, name := range sectorContextGroups {
		old := groupWindow(t, Report{Groups: previous.Groups}, name, 5)
		now := groupWindow(t, Report{Groups: current.Groups}, name, 5)
		if old.Selected != now.Selected {
			t.Fatalf("future evidence changed %s selected", name)
		}
	}
	priorFirst := groupWindow(t, Report{Groups: previous.Groups}, "first_all_red", 5)
	nowFirst := groupWindow(t, Report{Groups: current.Groups}, "first_all_red", 5)
	if nowFirst.Known >= priorFirst.Known || nowFirst.Selected != 12 || nowFirst.EntryUnknown < 6 {
		t.Fatalf("fixture must change outcome coverage while keeping priors: %+v vs %+v", priorFirst, nowFirst)
	}
}

func TestSectorContextCurrentOnlyNeverEnablesHistoricalCandidates(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	m.Through = m.From
	sectors.Basis, sectors.PublishedOn = "current_only", ""
	var events []Event
	_, report, err := AnalyzeSectorContext(context.Background(), m, source, sectors, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if report.HistoricalEligible || report.EligibleSignalDates != 0 || report.Audit.Classified != 12 || report.Audit.HistoricalEligible != 0 {
		t.Fatal("current classification claimed historical availability")
	}
	for _, name := range []string{stockOutperformSector, stockNotOutperformSector, sectorOutperformMarket, sectorNotOutperformMarket} {
		if groupWindow(t, Report{Groups: report.Groups}, name, 5).Selected != 0 {
			t.Fatalf("current-only snapshot selected historical %s", name)
		}
	}
	if groupWindow(t, Report{Groups: report.Groups}, contextUnknown, 5).Selected != 12 {
		t.Fatal("current-only observations disappeared from unknown denominator")
	}
	for _, event := range events {
		c := event.Context
		if c.ClassificationStatus != "classified" || c.HistoricalEligible || c.StockStage != "unknown" || c.SectorStage != "unknown" || c.StockStageReason != "classification_current_only" || c.StockMinusSectorPct == nil {
			t.Fatalf("descriptive attribution and selection mixed: %+v", c)
		}
	}
}

func TestSectorContextMissingAndNotYetPublicClassificationRetainDenominators(t *testing.T) {
	for _, mode := range []string{"missing", "same_publication_day"} {
		t.Run(mode, func(t *testing.T) {
			m, source, sectors := sectorStudyFixture(t)
			m.Through = m.From
			wantUnknown, reason := 1, "missing_membership"
			if mode == "missing" {
				sectors.Memberships = sectors.Memberships[1:]
			} else {
				sectors.PublishedOn = m.From
				wantUnknown, reason = 12, "classification_not_yet_public"
			}
			base, report, err := AnalyzeSectorContext(context.Background(), m, source, sectors, nil)
			if err != nil {
				t.Fatal(err)
			}
			if groupWindow(t, base, "first_all_red", 5).Selected != 12 || report.Audit.ClassificationUnknown != wantUnknown || report.Audit.FirstUnknownReasons[reason] != wantUnknown {
				t.Fatalf("classification failure removed signals: %+v", report.Audit)
			}
			for _, row := range report.MatchedSectorComparisons {
				if row.Group == "first_all_red" && row.Days == 5 {
					if row.Selected != 12 || row.CandidateKnown != 12 || row.UnmatchedReasons["sector_classification_unknown"] != wantUnknown || row.MatchedKnown+row.UnmatchedKnown != row.CandidateKnown {
						t.Fatalf("classification unknown excluded from matching denominator: %+v", row)
					}
				}
			}
		})
	}
}

func TestSectorReturnRequiresEveryExactPriorSession(t *testing.T) {
	m, source := studyV2Fixture(t)
	bars := append([]market.Bar(nil), source.pairs["sz000001"].Adjusted[:81]...)
	want := (bars[80].Close/bars[60].Close - 1) * 100
	if value, reason := sectorReturn20(bars, m.Calendar.Days, 80); reason != "" || value != want {
		t.Fatal(value, reason)
	}
	missing := append(append([]market.Bar(nil), bars[:70]...), bars[71:]...)
	if _, reason := sectorReturn20(missing, m.Calendar.Days, 80); reason != "return20_session_missing" {
		t.Fatal("a middle gap passed endpoint-only validation", reason)
	}
	if _, reason := sectorReturn20(bars[61:], m.Calendar.Days, 80); reason != "return20_history_missing" {
		t.Fatal("accepted fewer than 21 observations", reason)
	}
	bars[70].Close = math.NaN()
	if _, reason := sectorReturn20(bars, m.Calendar.Days, 80); reason != "return20_price_invalid" {
		t.Fatal("invalid prior price accepted", reason)
	}
}

func TestSectorMeansExcludeSelfAndKeepIndependentUnknownStages(t *testing.T) {
	m, _, sectors := sectorStudyFixture(t)
	day := &sectorPriorDay{sectors: map[string]*sectorMean{"A": {}}}
	for _, value := range []float64{50, 10, 10, 10, 10, 10} {
		day.market.add(value)
		day.sectors["A"].add(value)
	}
	for range 6 {
		day.market.add(0)
	}
	c := buildSectorSignalContext(sectors, "sz000001", m.From, sectorPriorFeature{selected: true, known: true, value: 50}, day)
	if c.SectorOtherStocks != 5 || *c.SectorOtherReturn20Pct != 10 || c.MarketOtherStocks != 11 || math.Abs(*c.MarketOtherReturn20Pct-50.0/11) > 1e-12 || c.StockStage != "outperform" || c.SectorStage != "outperform" {
		t.Fatalf("price peers included target or industry weights: %+v", c)
	}
	// An unavailable own return does not erase the independent industry stage.
	unknown := buildSectorSignalContext(sectors, "sz000001", m.From, sectorPriorFeature{selected: true, reason: "return20_session_missing"}, day)
	if unknown.StockStage != "unknown" || unknown.SectorStage != "outperform" || unknown.SectorOtherStocks != 6 {
		t.Fatal("independent sector stage incorrectly required own return", unknown)
	}
	event := Event{Groups: []string{"first_all_red"}, Context: &unknown}
	names := sectorNamesForEvent(event)
	if !hasSectorGroup(names, contextUnknown) || !hasSectorGroup(names, sectorOutperformMarket) || hasSectorGroup(names, stockOutperformSector) {
		t.Fatal("partial context incorrectly combined conditions", names)
	}
	var equal sectorMean
	for range 6 {
		equal.add(1.234567890123)
	}
	mean, n := equal.excluding(sectorPriorFeature{known: true, value: 1.234567890123})
	if n != 5 || *mean != 1.234567890123 || relativeSectorStage(1.234567890123-*mean) != "not_outperform" {
		t.Fatal("identical peers gained a floating-point positive stage")
	}
	equal.count--
	if mean, n = equal.excluding(sectorPriorFeature{known: true, value: 1.234567890123}); mean != nil || n != 4 {
		t.Fatal("four peers passed the five-other-stock coverage minimum")
	}
}

func TestSectorMatchingUsesCandidateCompositionSelfExclusionAndEqualDates(t *testing.T) {
	count := func(selected, known, success int) *sectorMatchCount {
		return &sectorMatchCount{selected: selected, known: [2]int{known, known}, success: [2]int{success, success}}
	}
	dates := []string{"2026-07-01", "2026-07-02", "2026-07-03"}
	grid := sectorMatchGrid{
		dates[0]: {
			"A": {"baseline": count(10, 10, 8), "first_all_red": count(7, 5, 3)},
			"B": {"baseline": count(10, 10, 2), "first_all_red": count(1, 1, 1)},
		},
		dates[1]: {
			"A": {"baseline": count(6, 6, 3), "first_all_red": count(1, 1, 1)},
			"C": {"baseline": count(5, 5, 2), "first_all_red": count(3, 2, 1)},
			"":  {"baseline": count(10, 10, 5), "first_all_red": count(2, 1, 1)},
		},
	}
	rows, err := grid.compare(context.Background(), dates, []string{"first_all_red"}, "2026-07")
	if err != nil {
		t.Fatal(err)
	}
	// Day 1: A contributes 5 candidates and B contributes 1, each against
	// their own leave-one-out peers. Day 2 gets equal date weight despite
	// having just one matched candidate. Day 3 stays an unpaired date.
	want := (-1.0/27 + .6) / 2
	for _, row := range rows {
		if row.Selected != 14 || row.CandidateKnown != 10 || row.MatchedKnown != 7 || row.UnmatchedKnown != 3 || row.PairedDates != 2 || row.UnpairedDates != 1 || row.MeanDifference == nil || math.Abs(*row.MeanDifference-want) > 1e-12 {
			t.Fatalf("wrong composition, self exclusion, date weighting or denominator: %+v want=%v", row, want)
		}
		if *row.KnownMatchingCoverage != .7 || *row.EffectiveMatchingCoverage != .5 || row.UnmatchedReasons["sector_classification_unknown"] != 1 || row.UnmatchedReasons["insufficient_known_sector_peers"] != 2 {
			t.Fatalf("unknown matching coverage hidden: %+v", row)
		}
	}
}

type sectorFailSource struct {
	*testSource
	failAt int
	cancel context.CancelFunc
}

func (s *sectorFailSource) Load(ctx context.Context, stock config.Stock) (Pair, error) {
	if s.loads == s.failAt {
		if s.cancel != nil {
			s.cancel()
		} else {
			return Pair{}, errors.New("injected source failure")
		}
	}
	return s.testSource.Load(ctx, stock)
}

func TestSectorContextFailureNeverReturnsCompleteReport(t *testing.T) {
	for _, scenario := range []string{"first_pass", "second_pass", "cancel", "sink", "legacy", "invalid_snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			m, source, sectors := sectorStudyFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wrapper := &sectorFailSource{testSource: source, failAt: -1}
			var emit func(Event) error
			switch scenario {
			case "first_pass":
				wrapper.failAt = 1
			case "second_pass":
				wrapper.failAt = len(source.stocks)
			case "cancel":
				wrapper.failAt, wrapper.cancel = 1, cancel
			case "sink":
				emit = func(Event) error { return errors.New("injected sink failure") }
			case "legacy":
				m.Version = 1
			case "invalid_snapshot":
				sectors.Version = 99
			}
			base, report, err := AnalyzeSectorContext(ctx, m, wrapper, sectors, emit)
			if err == nil || report != nil || !reflect.DeepEqual(base, Report{}) {
				t.Fatalf("failure returned a partial success: report=%+v err=%v", report, err)
			}
		})
	}
}
