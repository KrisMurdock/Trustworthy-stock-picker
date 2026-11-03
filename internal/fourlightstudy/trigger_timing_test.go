package fourlightstudy

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestTriggerTimingPreservesReportsEventsAndPartitions(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	var oldEvents, newEvents []Event
	base, industry, err := AnalyzeSectorContext(context.Background(), m, source, sectors, func(e Event) error { oldEvents = append(oldEvents, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	got, gotIndustry, report, err := AnalyzeTriggerTiming(context.Background(), m, source, sectors, func(e Event) error { newEvents = append(newEvents, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base, got) || !reflect.DeepEqual(industry, gotIndustry) {
		t.Fatal("trigger analysis changed base or industry report")
	}
	if report.Status != "complete" || report.Policy != TriggerContextPolicy || report.Audit.AllRedEvents != base.Audit.AllRedEventRows || len(newEvents) != len(oldEvents) {
		t.Fatal("incomplete timing audit")
	}
	for i, e := range newEvents {
		if e.Trigger == nil || !e.FourLight.AllRed {
			t.Fatal("lost trigger evidence or emitted non-all-red")
		}
		if hasSectorGroup(e.Groups, "first_all_red") {
			if e.Trigger.Lifecycle != "first_all_red" {
				t.Fatal("inconsistent lifecycle")
			}
			if e.Trigger.PriceAgeGroup == "0" && !hasSectorGroup(e.Groups, "first_price_involved") {
				t.Fatal("zero age not a price trigger")
			}
			if e.Trigger.PriceAgeGroup != "0" && e.Trigger.PriceAgeGroup != "unknown" && !hasSectorGroup(e.Groups, "first_energy_only") {
				t.Fatal("positive age not energy trigger")
			}
		}
		e.Trigger = nil
		if !reflect.DeepEqual(e, oldEvents[i]) {
			t.Fatal("original event changed")
		}
	}
	for _, days := range []int{5, 10} {
		for _, name := range []string{"baseline", "first_all_red", moderateVolumeGroup, "first_energy_only", "first_price_involved"} {
			if !reflect.DeepEqual(groupWindow(t, base, name, days), groupWindow(t, Report{Groups: report.Groups}, name, days)) {
				t.Fatal("original denominator changed", name)
			}
		}
		first := groupWindow(t, base, "first_all_red", days)
		for _, names := range [][]string{triggerAgeGroups, triggerPathGroups} {
			selected, known, success := 0, 0, 0
			for _, name := range names {
				w := groupWindow(t, Report{Groups: report.Groups}, name, days)
				selected += w.Selected
				known += w.Known
				success += w.Success
			}
			if selected != first.Selected || known != first.Known || success != first.Success {
				t.Fatal("partition lost selected, unknown or outcomes")
			}
		}
	}
	if len(report.EnergyComparisons) != 6 || len(report.MonthlyComparisons) != 1 {
		t.Fatal("missing energy reference or monthly comparison")
	}
}

func TestTriggerTimingFutureCoverageDoesNotSelectCandidates(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	m.Through = m.From
	var before, after []Event
	_, _, old, err := AnalyzeTriggerTiming(context.Background(), m, source, sectors, func(e Event) error { before = append(before, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for code, pair := range source.pairs {
		pair.Raw = pair.Raw[:81]
		pair.RawShares = pair.RawShares[:81]
		source.pairs[code] = pair
	}
	_, _, now, err := AnalyzeTriggerTiming(context.Background(), m, source, sectors, func(e Event) error { after = append(after, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("future changed signal count")
	}
	for i := range before {
		if !reflect.DeepEqual(before[i].Trigger, after[i].Trigger) || !reflect.DeepEqual(before[i].Groups, after[i].Groups) {
			t.Fatal("future availability changed timing features")
		}
	}
	for _, name := range triggerGroupNames() {
		if groupWindow(t, Report{Groups: old.Groups}, name, 5).Selected != groupWindow(t, Report{Groups: now.Groups}, name, 5).Selected {
			t.Fatal("future selected group", name)
		}
	}
	if groupWindow(t, Report{Groups: now.Groups}, "first_all_red", 5).Known != 0 {
		t.Fatal("fixture did not remove outcomes")
	}
}

func TestTriggerTimingUnknownAgeAndPathStayInDenominator(t *testing.T) {
	m, source, sectors := sectorStudyFixture(t)
	m.Through = m.From
	pair := source.pairs["sz000001"]
	pair.Adjusted = append(pair.Adjusted[:77:77], pair.Adjusted[78:]...)
	pair.AdjustedShares = append(pair.AdjustedShares[:77:77], pair.AdjustedShares[78:]...)
	source.pairs["sz000001"] = pair
	base, _, r, err := AnalyzeTriggerTiming(context.Background(), m, source, sectors, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Audit.FirstAllRed != 12 || r.Audit.AgeUnknown != 1 || r.Audit.PathUnknown != 1 {
		t.Fatalf("missing signal discarded: %+v", r.Audit)
	}
	for _, name := range []string{"first_price_age_unknown", "first_prior_path_unknown"} {
		if groupWindow(t, Report{Groups: r.Groups}, name, 5).Selected != 1 {
			t.Fatal("unknown removed", name)
		}
	}
	if groupWindow(t, base, "first_all_red", 5).Selected != 12 {
		t.Fatal("base denominator changed")
	}
}

func TestTriggerTimingErrorsDoNotReturnPartialReports(t *testing.T) {
	for _, mode := range []string{"source", "sink", "cancel", "legacy"} {
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
			}
			base, sector, trigger, err := AnalyzeTriggerTiming(ctx, m, source, sectors, sink)
			if err == nil || sector != nil || trigger != nil || !reflect.DeepEqual(base, Report{}) {
				t.Fatal("failure returned partial success")
			}
		})
	}
}

func TestTriggerLaggedEvidenceIgnoresSignalPriceForFixedEvent(t *testing.T) {
	bars, days, evidence := triggerFixture(t, 80, 73)
	before := AnalyzeTriggerContext(bars, days, evidence)
	bars[80].Open, bars[80].Close, bars[80].High, bars[80].Low, bars[80].Volume = 999, 1, 1000, .5, 999999
	after := AnalyzeTriggerContext(bars, days, evidence)
	if !reflect.DeepEqual(before.PriorRedRuns, after.PriorRedRuns) || !reflect.DeepEqual(before.Prior5Pct, after.Prior5Pct) || !reflect.DeepEqual(before.Prior20Pct, after.Prior20Pct) || before.PriorPathGroup != after.PriorPathGroup || before.PriceAgeGroup != after.PriceAgeGroup {
		t.Fatal("signal day leaked into lagged features")
	}
	if reflect.DeepEqual(before.SignalChangePct, after.SignalChangePct) {
		t.Fatal("fixture failed to change separately reported signal return")
	}
}
