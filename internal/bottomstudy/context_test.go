package bottomstudy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"stock-watcher/internal/config"
	"stock-watcher/internal/experiment"
	"stock-watcher/internal/market"
	"testing"
	"time"
)

func contextFixture(s experiment.Snapshot) MarketContext {
	c := MarketContext{Version: 1, StockSnapshotID: s.ID, Basis: "current_only", ObservedAt: "2026-10-16T00:00:00Z", From: s.Adjusted[0].Bars[0].Date, Through: s.AsOf}
	for _, code := range append(append([]string(nil), marketCodes...), "BK1") {
		kind, name := "market", code
		if code == "BK1" {
			kind, name = "sector", "样本行业"
		}
		c.Indices = append(c.Indices, ContextIndex{Code: code, Name: name, Kind: kind, Source: "fixture", Bars: append([]market.Bar(nil), s.Adjusted[0].Bars...)})
	}
	for _, x := range s.Stocks {
		c.Memberships = append(c.Memberships, Membership{StockCode: x.Code, SectorCode: "BK1", SectorName: "样本行业", ObservedAt: c.ObservedAt})
	}
	return c
}

func manualLookup(codes []string, date string) contextLookup {
	l := contextLookup{basis: "current_only", members: map[string][]Membership{}, features: map[string]map[string]ContextFeature{}}
	for _, code := range codes {
		l.members[code] = []Membership{{StockCode: code, SectorCode: "BK1", SectorName: "行业"}}
	}
	l.features["BK1"] = map[string]ContextFeature{date: {Date: date, Close: 10, MA5: 10, MA20: 11, RSI: 40, MACDRising: true, RSIRising: true, RecentBottom: true}}
	for _, code := range marketCodes {
		l.features[code] = map[string]ContextFeature{date: {Date: date, MarketScore: 0}}
	}
	return l
}

func TestSectorReadinessAndWeakMarketRanking(t *testing.T) {
	date := "2026-05-12"
	codes := []string{"sh600001", "sz300001"}
	l := manualLookup(codes, date)
	f := l.features["sz399006"][date]
	f.MarketScore = 100
	l.features["sz399006"][date] = f
	a, b := l.at(codes[0], date), l.at(codes[1], date)
	if !a.Known || !a.SectorEligible || !b.SectorEligible || b.MarketScore <= a.MarketScore || .1*b.MarketScore > 10 {
		t.Fatal("market weak scoring", a, b)
	}
	opps := []opportunity{}
	features := map[string]map[string]ContextFeature{}
	for _, code := range codes {
		opps = append(opps, opportunity{id: code + "/" + date, code: code, split: "training", bars: []market.Bar{{Date: date}}})
		features[code] = map[string]ContextFeature{date: {TrendScore: 20}}
	}
	only := selectSignals(opps, features, l, "stock_all", 1)
	combined := selectSignals(opps, features, l, "stock_sector_market", 1)
	if !only[0].Selected || only[1].Selected || combined[0].Selected || !combined[1].Selected {
		t.Fatal("weak market must affect tied ranks", only, combined)
	}
	// A negative market is never a gate: the Shanghai stock remains admissible.
	if !selectSignals(opps[:1], features, l, "stock_sector_market", 1)[0].Selected {
		t.Fatal("market vetoed an otherwise eligible trade")
	}
	sector := l.features["BK1"][date]
	sector.MACDRising, sector.RSIRising = false, false
	l.features["BK1"][date] = sector
	if l.at(codes[0], date).SectorEligible {
		t.Fatal("bottom event with continued weakness passed")
	}
	if d := selectSignals(opps, features, l, "stock_sector_market", 1)[0]; d.Selected || d.Reason != "sector_not_ready" {
		t.Fatal(d)
	}
	delete(l.members, codes[0])
	if l.at(codes[0], date).Known {
		t.Fatal("missing membership was considered favorable")
	}
}

func TestContextRejectsAmbiguousDatesAndMalformedData(t *testing.T) {
	s := gridFixture(t, false)
	o := GridOptions{Options{"2026-01-01", s.AsOf}, "2026-06-30"}
	for _, test := range []struct {
		name   string
		change func(*MarketContext)
	}{
		{"wrong snapshot", func(c *MarketContext) { c.StockSnapshotID = "wrong" }},
		{"duplicate index", func(c *MarketContext) { c.Indices = append(c.Indices, c.Indices[0]) }},
		{"missing market", func(c *MarketContext) { c.Indices = c.Indices[1:] }},
		{"duplicate current membership", func(c *MarketContext) { c.Memberships = append(c.Memberships, c.Memberships[0]) }},
		{"undated historical membership", func(c *MarketContext) { c.Basis = "dated" }},
		{"current pretends historical", func(c *MarketContext) { c.Memberships[0].From = "2020-01-01" }},
		{"bad OHLC", func(c *MarketContext) { c.Indices[0].Bars[80].High = 0 }},
		{"missing session", func(c *MarketContext) { c.Indices[0].Bars = append(c.Indices[0].Bars[:80], c.Indices[0].Bars[81:]...) }},
		{"unknown stock", func(c *MarketContext) { c.Memberships[0].StockCode = "sh999999" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := contextFixture(s)
			test.change(&c)
			if validateContext(c, s, o) == nil {
				t.Fatal("accepted malformed context")
			}
		})
	}
	c := contextFixture(s)
	c.Basis = "dated"
	for i := range c.Memberships {
		c.Memberships[i].From, c.Memberships[i].Through = "2026-04-01", s.AsOf
	}
	if err := validateContext(c, s, o); err != nil {
		t.Fatal(err)
	}
	lookup, err := buildContext(context.Background(), c, s.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if lookup.at(s.Stocks[0].Code, "2026-03-30").Known {
		t.Fatal("membership used before its effective date")
	}
	if !lookup.at(s.Stocks[0].Code, "2026-04-02").Known {
		t.Fatal("effective dated membership missing")
	}
}

func TestCombinedAddFilterCopiesEventsAndKeepsMissingUnresolved(t *testing.T) {
	bars := fixture(22).Bars
	code := "sh600001"
	o := opportunity{id: "one", code: code, bars: bars, events: make([]bool, 22), invalid: map[int]string{}}
	o.events[0], o.events[2], o.events[4] = true, true, true
	l := manualLookup([]string{code}, bars[0].Date)
	// All subsequent context is missing. Its absence must not mutate A or
	// silently imply favorable conditions for B's future add decisions.
	out := selectedOpportunities([]opportunity{o}, []SelectionDecision{{Selected: true}}, l, true)
	if !o.events[2] || len(o.unknownAdds) != 0 || !out[0].events[2] || !out[0].unknownAdds[3] {
		t.Fatal("invalid add isolation", o, out)
	}
	fees := config.Defaults().Strategy
	fees.TradeCash = 100000
	p := TradePolicy{Rule: "any", Batches: 2, Horizon: 5, Target: 3}
	if tr := simulateTrade(fees, p, out[0]); tr.Status != "missing_add_context" {
		t.Fatal("missing needed add context did not remain unknown", tr)
	}
	p.Batches = 1
	if tr := simulateTrade(fees, p, out[0]); tr.Status != "timeout" {
		t.Fatal("irrelevant add context blocked a fully funded position", tr)
	}
	p.Batches, p.Horizon = 2, 2
	if tr := simulateTrade(fees, p, out[0]); tr.Status != "timeout" {
		t.Fatal("irrelevant add context blocked deadline exit", tr)
	}
	p.Horizon = 5
	modified := out[0]
	modified.bars = append([]market.Bar(nil), out[0].bars...)
	modified.bars[3].Open, modified.bars[3].High, modified.bars[3].Close = 110, 111, 110
	if tr := simulateTrade(fees, p, modified); tr.Status != "target" {
		t.Fatal("missing add context blocked known opening target", tr)
	}
}

func TestComparisonCausalTrainingDeterminismAndMissingCoverage(t *testing.T) {
	s := gridFixture(t, false)
	c := contextFixture(s)
	o := GridOptions{Options{"2026-01-01", s.AsOf}, "2026-06-30"}
	a, err := CompareContext(context.Background(), s, c, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Pairs) != 220 {
		t.Fatal("missing paired grid", len(a.Pairs))
	}
	entriesA, entriesB := 0, 0
	for _, p := range a.Pairs {
		if !reflect.DeepEqual(p.StockAll, p.StockCommon) {
			t.Fatal("complete context altered A coverage")
		}
		entriesA += p.StockAll.Training.Entries
		entriesB += p.Combined.Training.Entries
	}
	if entriesA == 0 || entriesB == 0 {
		t.Fatal("fixture does not exercise both arms")
	}
	again, err := CompareContext(context.Background(), s, c, o)
	if err != nil {
		t.Fatal(err)
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(again)
	if string(aj) != string(bj) {
		t.Fatal("nondeterministic report")
	}
	changed := gridFixture(t, true)
	future := contextFixture(s)
	future.StockSnapshotID = changed.ID
	for i := range future.Indices {
		for j, b := range future.Indices[i].Bars {
			if b.Date > o.TrainThrough {
				b.Open *= 1.2
				b.Close *= 1.2
				b.High *= 1.2
				b.Low *= 1.2
				future.Indices[i].Bars[j] = b
			}
		}
	}
	b, err := CompareContext(context.Background(), changed, future, o)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Winners, b.Winners) {
		t.Fatal("future context changed training winners")
	}
	for i, p := range a.Pairs {
		q := b.Pairs[i]
		if !reflect.DeepEqual(p.StockAll.Training, q.StockAll.Training) || !reflect.DeepEqual(p.Combined.Training, q.Combined.Training) {
			t.Fatal("training leakage", p.ID)
		}
	}
	for i, sel := range a.Selections {
		for j, d := range sel.Decisions {
			if d.Split == "training" && !reflect.DeepEqual(d, b.Selections[i].Decisions[j]) {
				t.Fatal("future data altered signal-day decision")
			}
		}
	}
	missing := contextFixture(s)
	missing.Memberships = nil
	noCoverage, err := CompareContext(context.Background(), s, missing, o)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range noCoverage.Pairs {
		if !reflect.DeepEqual(p.StockAll.Training.TradeStats, a.Pairs[i].StockAll.Training.TradeStats) || p.Combined.Training.Samples != 0 || p.StockCommon.Training.Samples != 0 {
			t.Fatal("missing context hid full baseline or counted fake samples")
		}
	}
	for _, ctx := range []context.Context{func() context.Context { x, cancel := context.WithCancel(context.Background()); cancel(); return x }(), func() context.Context {
		x, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return x
	}()} {
		_, err := CompareContext(ctx, s, c, o)
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("cancellation lost", err)
		}
	}
}
