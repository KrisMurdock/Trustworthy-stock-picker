package fairvalue

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestCalculateV2ProducesTheoryConsistentScenarios(t *testing.T) {
	snapshot := testSnapshot()
	estimate := CalculateV2(config.Stock{Code: snapshot.Code, Sector: "白酒"}, 12, "2026-10-31", snapshot)
	if err := estimate.Validate(); err != nil {
		t.Fatal(err)
	}
	if !estimate.Available() || estimate.Confidence != "low" || estimate.DataQuality != DataQualityTTMOnly {
		t.Fatalf("unexpected availability or quality: %+v", estimate)
	}
	if math.Abs(estimate.ROEPct-10) > 1e-9 || math.Abs(estimate.GrowthPct-25) > 1e-9 || estimate.SustainableGrowthPct != 6 {
		t.Fatalf("unexpected profitability or growth: %+v", estimate)
	}
	if estimate.CostOfEquityPct != centralCostOfEquityPctV2 {
		t.Fatalf("unexpected central cost of equity: %+v", estimate)
	}
	if math.Abs(estimate.FairPE-estimate.FairPB/(estimate.ROEPct/100)) > 1e-9 {
		t.Fatalf("FairPE is not internally consistent: %+v", estimate)
	}
	if estimate.Lower > estimate.Central || estimate.Central > estimate.Upper {
		t.Fatalf("range is not ordered: %+v", estimate)
	}
	if len(estimate.Scenarios) != 6 {
		t.Fatalf("scenario count = %d, want 6 after boundary de-duplication", len(estimate.Scenarios))
	}
	matchedCentral := false
	for _, scenario := range estimate.Scenarios {
		if scenario.CostOfEquityPct-scenario.GrowthPct < 1 {
			t.Fatalf("scenario violates r-g constraint: %+v", scenario)
		}
		if scenario.GrowthPct < -2 || scenario.GrowthPct > 6 {
			t.Fatalf("scenario growth outside clamp: %+v", scenario)
		}
		if scenario.FairPB < 0.2 || scenario.FairPB > 8 || scenario.FairPE <= 0 || scenario.Price <= 0 {
			t.Fatalf("scenario multiple outside bounds: %+v", scenario)
		}
		if math.Abs(scenario.FairPE-scenario.FairPB/(estimate.ROEPct/100)) > 1e-9 {
			t.Fatalf("scenario FairPE is inconsistent: %+v", scenario)
		}
		if nearlyEqualV2(scenario.Price, estimate.Central) &&
			nearlyEqualV2(scenario.FairPB, estimate.FairPB) && nearlyEqualV2(scenario.FairPE, estimate.FairPE) {
			matchedCentral = true
		}
	}
	if !matchedCentral {
		t.Fatalf("central estimate does not match a scenario: %+v", estimate)
	}
	if math.Abs(estimate.UpsidePct-(estimate.Central/12-1)*100) > 1e-9 {
		t.Fatalf("upside is inconsistent: %+v", estimate)
	}
}

func TestCalculateV2LowROEProducesSubBookPB(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPS = 0.4
	snapshot.EPSPrior = 0.4
	snapshot.TTMNetProfitYi = 0
	snapshot.BookValuePerShare = 10
	estimate := CalculateV2(config.Stock{Code: snapshot.Code, Sector: "制造业"}, 8, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.ROEPct != 4 || estimate.FairPB >= 1 {
		t.Fatalf("low ROE did not produce FairPB below 1: %+v", estimate)
	}
	if math.Abs(estimate.FairPB-4.0/9.5) > 1e-9 {
		t.Fatalf("unexpected low-ROE FairPB: %+v", estimate)
	}
}

func TestCalculateV2CapsHighROEBySector(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.BookValuePerShare = 1
	nonFinancial := CalculateV2(config.Stock{Code: snapshot.Code, Sector: "软件服务"}, 20, "2026-10-31", snapshot)
	if !nonFinancial.Available() || nonFinancial.FairPB != 8 {
		t.Fatalf("non-financial PB cap missing: %+v", nonFinancial)
	}
	financial := CalculateV2(config.Stock{Code: snapshot.Code, Sector: "股份制银行"}, 20, "2026-10-31", snapshot)
	if !financial.Available() || financial.FairPB != 3 || financial.Confidence != "low" {
		t.Fatalf("financial PB cap or confidence missing: %+v", financial)
	}
}

func TestCalculateV2ExplicitlyHandlesNegativeFinancials(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*market.FundamentalSnapshot)
		warning string
		reason  string
	}{
		{
			name: "negative earnings",
			mutate: func(snapshot *market.FundamentalSnapshot) {
				snapshot.EPS = -1
				snapshot.TTMNetProfitYi = 0
			},
			warning: "non_positive_earnings",
			reason:  "每股收益非正",
		},
		{
			name: "negative book value",
			mutate: func(snapshot *market.FundamentalSnapshot) {
				snapshot.BookValuePerShare = -10
			},
			warning: "book_value_non_positive",
			reason:  "每股净资产非正",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := testSnapshot()
			test.mutate(&snapshot)
			estimate := CalculateV2(config.Stock{Code: snapshot.Code}, 10, "2026-10-31", snapshot)
			if estimate.Available() || !strings.Contains(estimate.Reason, test.reason) || !containsString(estimate.Warnings, test.warning) {
				t.Fatalf("unexpected unavailable estimate: %+v", estimate)
			}
		})
	}
}

func TestCalculateV2UnknownGrowthUsesZeroWithoutFabrication(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPSPrior = 0
	snapshot.GrowthKnown = false
	estimate := CalculateV2(config.Stock{Code: snapshot.Code, Sector: "白酒"}, 10, "2026-10-31", snapshot)
	if !estimate.Available() || estimate.GrowthPct != 0 || estimate.SustainableGrowthPct != 0 {
		t.Fatalf("unknown growth was not fixed at zero: %+v", estimate)
	}
	if !containsString(estimate.Warnings, "growth_unknown") || estimate.Confidence != "low" {
		t.Fatalf("unknown growth warning or confidence missing: %+v", estimate)
	}
}

func TestScenarioV2RejectsInsufficientRGSpread(t *testing.T) {
	if _, ok := makeScenarioV2(10, 8, 8.5, 10, false); ok {
		t.Fatal("scenario with r-g < 1 was accepted")
	}
	if _, ok := makeScenarioV2(5, 6, 9.5, 10, false); ok {
		t.Fatal("scenario with g > ROE was accepted")
	}
	scenario, ok := makeScenarioV2(10, 7, 8.5, 10, false)
	if !ok || scenario.FairPB <= 0 || scenario.FairPE <= 0 || scenario.Price <= 0 {
		t.Fatalf("valid r-g scenario rejected: %+v ok=%v", scenario, ok)
	}
}

func TestCalculateV2RejectsGrowthAboveROEInsteadOfFlooringPB(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPS = 0.02
	snapshot.EPSPrior = 0.01
	snapshot.TTMNetProfitYi = 0.000002
	snapshot.BookValuePerShare = 10
	estimate := CalculateV2(config.Stock{Code: snapshot.Code, Sector: "白酒"}, 10, "2026-10-31", snapshot)
	if estimate.Available() || estimate.Reason == "" || !strings.Contains(estimate.Reason, "增长率不低于净资产收益率") {
		t.Fatalf("growth above ROE produced a fabricated valuation: %+v", estimate)
	}
}

func TestCalculateV2RejectsGrowthEqualToROE(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPS = 0.5
	snapshot.EPSPrior = snapshot.EPS / 1.05
	snapshot.TTMNetProfitYi = 0.00005
	snapshot.BookValuePerShare = 10
	estimate := CalculateV2(config.Stock{Code: snapshot.Code, Sector: "白酒"}, 10, "2026-10-31", snapshot)
	if estimate.Available() || estimate.Reason == "" || !strings.Contains(estimate.Reason, "增长率不低于净资产收益率") {
		t.Fatalf("growth equal to ROE produced a fabricated valuation: %+v", estimate)
	}
	if err := estimate.Validate(); err != nil {
		t.Fatalf("explained unavailable estimate failed validation: %v", err)
	}
}

func TestCalculateV2InvalidSnapshotUsesUnavailableDataQuality(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Source = "invalid"
	estimate := CalculateV2(config.Stock{Code: snapshot.Code}, 10, "2026-10-31", snapshot)
	if estimate.Available() || estimate.DataQuality != DataQualityUnavailable || estimate.Reason == "" {
		t.Fatalf("invalid snapshot quality was not marked unavailable: %+v", estimate)
	}
	if err := estimate.Validate(); err != nil {
		t.Fatalf("unavailable v2 estimate failed validation: %v", err)
	}
}

func TestCalculateV2InvalidPriceUsesUnavailableDataQuality(t *testing.T) {
	estimate := CalculateV2(config.Stock{Code: "sh600519"}, 0, "2026-10-31", testSnapshot())
	if estimate.Available() || estimate.DataQuality != DataQualityUnavailable || estimate.Reason == "" {
		t.Fatalf("invalid price quality was not marked unavailable: %+v", estimate)
	}
}

func TestEstimateV2ValidateRejectsInvalidContracts(t *testing.T) {
	valid := CalculateV2(config.Stock{Code: "sh600519", Sector: "白酒"}, 12, "2026-10-31", testSnapshot())
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}

	unordered := valid
	unordered.Central = valid.Upper + 1
	unordered.Reason = "forced unavailable to isolate ordering"
	if err := unordered.Validate(); err == nil {
		t.Fatal("unordered range was accepted")
	}

	nonFinite := valid
	nonFinite.Lower = math.NaN()
	if err := nonFinite.Validate(); err == nil {
		t.Fatal("non-finite range was accepted")
	}

	scenarioNonFinite := valid
	scenarioNonFinite.Scenarios = append([]ScenarioV2(nil), valid.Scenarios...)
	scenarioNonFinite.Scenarios[0].Price = math.Inf(1)
	if err := scenarioNonFinite.Validate(); err == nil {
		t.Fatal("non-finite scenario was accepted")
	}

	unknownQuality := valid
	unknownQuality.DataQuality = "unknown"
	if err := unknownQuality.Validate(); err == nil {
		t.Fatal("unknown data quality was accepted")
	}

	invalidConstraint := valid
	invalidConstraint.Scenarios = append([]ScenarioV2(nil), valid.Scenarios...)
	invalidConstraint.Scenarios[0].GrowthPct = valid.ROEPct
	if err := invalidConstraint.Validate(); err == nil {
		t.Fatal("scenario with growth equal to ROE was accepted")
	}

	inconsistentScenario := valid
	inconsistentScenario.Scenarios = append([]ScenarioV2(nil), valid.Scenarios...)
	inconsistentScenario.Scenarios[0].Price *= 1.01
	if err := inconsistentScenario.Validate(); err == nil {
		t.Fatal("scenario with inconsistent price was accepted")
	}

	unavailableWithoutReason := EstimateV2{
		Version: VersionV2, Code: "sh600519", AsOf: "2026-10-31", Method: MethodV2,
	}
	if err := unavailableWithoutReason.Validate(); err == nil {
		t.Fatal("unavailable estimate without reason was accepted")
	}
}

func TestEstimateV2JSONContract(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.EPSPrior = 0
	snapshot.GrowthKnown = false
	estimate := CalculateV2(config.Stock{Code: "sh600519", Sector: "白酒"}, 12, "2026-10-31", snapshot)
	encoded, err := json.Marshal(estimate)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, key := range []string{
		`"schema_version"`, `"code"`, `"as_of"`, `"source"`, `"fetched_at"`, `"method"`,
		`"statement_basis"`, `"confidence"`, `"data_quality"`, `"warnings"`, `"current_price"`,
		`"eps_ttm"`, `"book_value_per_share"`, `"eps_growth_pct"`, `"roe_pct"`, `"current_pe"`,
		`"current_pb"`, `"sustainable_growth_pct"`, `"implied_payout_pct"`, `"cost_of_equity_pct"`,
		`"fair_pb"`, `"fair_pe"`, `"lower"`, `"central"`, `"upper"`, `"upside_pct"`, `"scenarios"`,
		`"growth_pct"`, `"price"`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("JSON contract missing %s: %s", key, text)
		}
	}
	if strings.Contains(text, `"payout_pct"`) || !strings.Contains(text, `"implied_payout_pct"`) {
		t.Fatalf("derived payout was exposed as an observed payout: %s", text)
	}
}
