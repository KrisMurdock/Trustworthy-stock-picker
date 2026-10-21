package firststage

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestGoldenParityWithExperimentModel(t *testing.T) {
	histories, pool, asof := parityFixture()
	report, err := Analyze(histories, pool, Options{
		AsOf:        asof,
		Target:      "2026-10-07",
		Adjust:      "qfq",
		Budget:      3,
		PushEnabled: true,
		Now:         time.Date(2026, 10, 6, 19, 0, 0, 0, market.Shanghai),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != StatusOK || report.Eligible != 3 || len(report.Candidates) != 3 || !report.PushEnabled {
		t.Fatalf("unexpected report: %+v", report)
	}
	want := []Candidate{
		{Rank: 1, Code: "sz000001", Name: "甲", Close: 26.8, BaseScore: 88.75, ModelScore: 0.6767201002789583, BaseRank: 1, ModelRank: 1, BlendScore: 1},
		{Rank: 2, Code: "sz000002", Name: "乙", Close: 28.1, BaseScore: 78.75, ModelScore: 0.5167815427286157, BaseRank: 0, ModelRank: 0.5, BlendScore: 0.25},
		{Rank: 3, Code: "sz000003", Name: "丙", Close: 29.4, BaseScore: 78.75, ModelScore: 0.35166398127921217, BaseRank: 0.5, ModelRank: 0, BlendScore: 0.25},
	}
	for i := range want {
		got := report.Candidates[i]
		if got.Rank != want[i].Rank || got.Code != want[i].Code || got.Name != want[i].Name {
			t.Fatalf("candidate %d identity = %+v, want %+v", i, got, want[i])
		}
		for _, pair := range [][2]float64{
			{got.Close, want[i].Close},
			{got.BaseScore, want[i].BaseScore},
			{got.ModelScore, want[i].ModelScore},
			{got.BaseRank, want[i].BaseRank},
			{got.ModelRank, want[i].ModelRank},
			{got.BlendScore, want[i].BlendScore},
		} {
			if math.Abs(pair[0]-pair[1]) > 1e-9 {
				t.Fatalf("candidate %d score %v != %v: %+v", i, pair[0], pair[1], got)
			}
		}
	}
	if report.ModelID != ModelID || report.ModelVersion != ModelSchemaVersion || report.ModelSHA256 != modelArtifactSHA256 || report.TrainedThrough != "2026-10-15" {
		t.Fatalf("missing model provenance: %+v", report)
	}
}

func TestBudgetExclusionAndIdentityFailures(t *testing.T) {
	histories, pool, asof := parityFixture()
	options := Options{AsOf: asof, Target: "2026-10-07", Adjust: "qfq", Budget: 2, Now: time.Now()}
	report, err := Analyze(histories, pool, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Candidates) != 2 || len(report.Excluded) != 1 || report.Excluded[0].Code != "sz000003" || report.Excluded[0].Reason != "outside_budget" {
		t.Fatalf("unexpected budget exclusion: %+v", report)
	}
	if _, err := Analyze(append(histories, histories[0]), pool, options); err == nil {
		t.Fatal("duplicate history accepted")
	}
	if _, err := Analyze(histories, append(pool, pool[0]), options); err == nil {
		t.Fatal("duplicate pool accepted")
	}
}

func TestExclusionBranchesKeepMainReportDecisionUsable(t *testing.T) {
	histories, _, asof := parityFixture()
	valid := histories[0]
	missing := config.Stock{Code: "sz000004", Name: "缺失"}
	short := copyHistory(valid, "sz000002", "短历史")
	short.Bars = append([]market.Bar(nil), valid.Bars[:64]...)
	short.AsOf = asof
	stale := copyHistory(valid, "sz000003", "过期")
	stale.Bars = parityBarsFrom(0, time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC))
	stale.AsOf = asof
	report, err := Analyze([]market.History{valid, short, stale}, []config.Stock{validPoolStock(), missing, {Code: "sz000002", Name: "短历史"}, {Code: "sz000003", Name: "过期"}}, Options{AsOf: asof, Target: "2026-10-07", Adjust: "qfq", Budget: 10, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, item := range report.Excluded {
		reasons[item.Code] = item.Reason
	}
	if reasons[missing.Code] != "missing_data" || reasons["sz000002"] != "insufficient_history" || reasons["sz000003"] != "stale_or_suspended" {
		t.Fatalf("unexpected exclusion reasons: %+v", report.Excluded)
	}
	if len(report.Candidates) != 1 || report.Eligible != 1 || report.Candidates[0].Code != valid.Code {
		t.Fatalf("valid stock was lost: %+v", report)
	}
}

func TestModelArtifactValidation(t *testing.T) {
	model, err := parseModel(modelArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if model.ID != ModelID || model.SchemaVersion != ModelSchemaVersion || len(model.Coefficients) != len(modelFeatures) {
		t.Fatalf("unexpected model: %+v", model)
	}
	if err := verifyChecksum(modelArtifact, "deadbeef"); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if _, err := parseModel([]byte(strings.Replace(string(modelArtifact), `"z_atr14_pct"`, `"z_unknown"`, 1))); err == nil {
		t.Fatal("unknown feature order accepted")
	}
	if _, err := parseModel([]byte(strings.Replace(string(modelArtifact), `"scales": [`, `"scales": [0, `, 1))); err == nil {
		t.Fatal("invalid parameter length accepted")
	}
	if _, err := parseModel([]byte(strings.Replace(string(modelArtifact), `{`, `{"unknown": 1,`, 1))); err == nil {
		t.Fatal("unknown model field accepted")
	}
}

func parityFixture() ([]market.History, []config.Stock, string) {
	codes := []struct{ code, name string }{{"sz000001", "甲"}, {"sz000002", "乙"}, {"sz000003", "丙"}}
	histories := make([]market.History, 0, len(codes))
	pool := make([]config.Stock, 0, len(codes))
	for index, item := range codes {
		bars := parityBars(index)
		history := market.History{
			Version: 1, Code: item.code, Name: item.name, Source: "tencent", Adjust: "qfq",
			DataField: "qfqday", VolumeUnit: "provider_raw", AsOf: bars[len(bars)-1].Date,
			FetchedAt: time.Date(2026, 10, 6, 15, 0, 0, 0, market.Shanghai), Bars: bars,
		}
		histories = append(histories, history)
		pool = append(pool, config.Stock{Code: item.code, Name: item.name})
	}
	return histories, pool, histories[0].AsOf
}

func parityBars(stock int) []market.Bar {
	return parityBarsFrom(stock, time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC))
}

func parityBarsFrom(stock int, start time.Time) []market.Bar {
	bars := make([]market.Bar, 0, 65)
	day := start
	for len(bars) < 65 {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			day = day.AddDate(0, 0, 1)
			continue
		}
		i := len(bars)
		close := 20 + 0.10*float64(i) + 0.5*float64(i%11-5)/5 + float64(stock)*1.3
		open := close - 0.05*float64(i%7-3)
		high := math.Max(open, close) + 0.2 + 0.03*float64(i%5)
		low := math.Min(open, close) - 0.18 - 0.02*float64((i+2)%4)
		volume := 100000 + 2000*float64(i%13) + float64(stock)*5000
		if i == 64 {
			volume += 18000 + float64(stock)*1000
		}
		bars = append(bars, market.Bar{Date: day.Format("2006-01-02"), Open: open, High: high, Low: low, Close: close, Volume: volume})
		day = day.AddDate(0, 0, 1)
	}
	return bars
}

func copyHistory(source market.History, code, name string) market.History {
	source.Code = code
	source.Name = name
	source.Bars = append([]market.Bar(nil), source.Bars...)
	return source
}

func validPoolStock() config.Stock { return config.Stock{Code: "sz000001", Name: "甲"} }
