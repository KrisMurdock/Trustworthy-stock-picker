package fundamentals

import (
	"fmt"
	"math"
	"testing"
	"time"
)

const testCode = "sh600000"

func number(value float64) *float64 { return &value }

func testPeriod(year int, netProfit, equity, cashDividend *float64) Period {
	return testPeriodAvailable(year, netProfit, equity, cashDividend,
		fmt.Sprintf("%d-04-20", year+1), fmt.Sprintf("%d-04-21", year+1),
		fmt.Sprintf("rev-%d", year), fmt.Sprintf("row-%d", year))
}

func testPeriodAvailable(year int, netProfit, equity, cashDividend *float64, announced, available, revision, rowHash string) Period {
	fetched, _ := parseDate(available)
	return Period{
		SchemaVersion:    Version,
		Code:             testCode,
		Source:           SourceTushare,
		ReportEnd:        fmt.Sprintf("%d-12-31", year),
		AnnouncementDate: announced,
		AvailableFrom:    available,
		StatementBasis:   StatementAnnualConsolidated,
		ReportType:       "1",
		CompType:         1,
		UpdateFlag:       0,
		Revision:         revision,
		FetchedAt:        fetched.Add(12 * time.Hour),
		RowHash:          rowHash,
		NetProfit:        netProfit,
		Equity:           equity,
		TotalShares:      number(1000),
		CashDividend:     cashDividend,
	}
}

func testSeries(periods ...Period) Series {
	fetched := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, period := range periods {
		if period.FetchedAt.After(fetched) {
			fetched = period.FetchedAt
		}
	}
	return Series{
		SchemaVersion: Version,
		Code:          testCode,
		Source:        SourceTushare,
		FetchedAt:     fetched,
		Periods:       periods,
	}
}

func seriesForYears(start, end int) Series {
	periods := make([]Period, 0, end-start+1)
	for year := start; year <= end; year++ {
		periods = append(periods, testPeriod(year, number(100), number(1000), nil))
	}
	return testSeries(periods...)
}

func TestAnnualVisibleExcludesSameDayAnnouncement(t *testing.T) {
	series := seriesForYears(2019, 2023)
	if got := len(series.AnnualVisible("2024-04-20")); got != 4 {
		t.Fatalf("same-day visible periods = %d, want 4", got)
	}
	if got := len(series.AnnualVisible("2024-04-21")); got != 5 {
		t.Fatalf("next-day visible periods = %d, want 5", got)
	}
}

func TestAnnualVisibleSelectsLatestVisibleRevision(t *testing.T) {
	periods := []Period{
		testPeriod(2019, number(10), number(100), nil),
		testPeriod(2020, number(11), number(110), nil),
		testPeriod(2021, number(12), number(120), nil),
		testPeriod(2022, number(13), number(130), nil),
		testPeriodAvailable(2023, number(11.5), number(140), nil, "2024-04-20", "2024-04-21", "rev-2023", "row-2023-a"),
		testPeriodAvailable(2023, number(12.5), number(145), nil, "2024-05-31", "2024-06-01", "rev-2023-b", "row-2023-b"),
	}
	series := testSeries(periods...)

	early := series.AnnualVisible("2024-05-01")
	if got := *early[len(early)-1].NetProfit; got != 11.5 {
		t.Fatalf("early revision net profit = %v, want 11.5", got)
	}
	late := series.AnnualVisible("2024-06-01")
	if got := *late[len(late)-1].NetProfit; got != 12.5 {
		t.Fatalf("late revision net profit = %v, want 12.5", got)
	}

	series.Periods = append(series.Periods,
		testPeriodAvailable(2023, number(99), number(150), nil, "2025-01-01", "2025-01-02", "rev-2023-future", "row-2023-future"))
	stillEarly := series.AnnualVisible("2024-05-01")
	if got := *stillEarly[len(stillEarly)-1].NetProfit; got != 11.5 {
		t.Fatalf("future revision polluted early as-of: %v", got)
	}
}

func TestAnnualVisibleObservedExcludesFutureFetch(t *testing.T) {
	series := seriesForYears(2019, 2023)
	for index := range series.Periods {
		series.Periods[index].FetchedAt = time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	}
	if got := len(series.AnnualVisible("2024-05-01")); got != 0 {
		t.Fatalf("future-fetched rows entered observed view: %d", got)
	}
	if got := len(series.AnnualVisibleVendorBackfill("2024-05-01")); got != 5 {
		t.Fatalf("vendor backfill view = %d periods, want 5", got)
	}
	if series.Periods[0].ObservedBy("2026-01-02") != true || series.Periods[0].ObservedBy("2026-01-01") != false {
		t.Fatal("ObservedBy did not compare the Shanghai fetch date")
	}
}

func TestAnnualVisibleCorrectionCanMoveAvailableDateEarlier(t *testing.T) {
	old := testPeriodAvailable(2023, number(11.5), number(140), nil,
		"2024-04-20", "2024-04-23", "rev-2023", "row-2023-old")
	old.FetchedAt = time.Date(2024, 4, 20, 12, 0, 0, 0, time.UTC)
	corrected := old
	corrected.NetProfit = number(12.5)
	corrected.AvailableFrom = "2024-04-22"
	corrected.FetchedAt = time.Date(2024, 4, 21, 12, 0, 0, 0, time.UTC)
	corrected.RowHash = "row-2023-corrected"
	series := testSeries(old, corrected)

	atCorrection := series.AnnualVisible("2024-04-22")
	if len(atCorrection) != 1 || *atCorrection[0].NetProfit != 12.5 {
		t.Fatalf("corrected earlier availability was not visible: %+v", atCorrection)
	}
	later := series.AnnualVisible("2024-04-23")
	if len(later) != 1 || *later[0].NetProfit != 12.5 {
		t.Fatalf("older row overrode the later observation: %+v", later)
	}

	future := corrected
	future.NetProfit = number(13.5)
	future.AvailableFrom = "2024-04-22"
	future.FetchedAt = time.Date(2024, 4, 24, 12, 0, 0, 0, time.UTC)
	future.RowHash = "row-2023-future-correction"
	series = testSeries(old, corrected, future)
	beforeFuture := series.AnnualVisible("2024-04-23")
	if len(beforeFuture) != 1 || *beforeFuture[0].NetProfit != 12.5 {
		t.Fatalf("future correction leaked into earlier as-of: %+v", beforeFuture)
	}
	afterFuture := series.AnnualVisible("2024-04-24")
	if len(afterFuture) != 1 || *afterFuture[0].NetProfit != 13.5 {
		t.Fatalf("later observed correction did not win: %+v", afterFuture)
	}
}

func TestSeriesBatchIDIsDeterministicAndFetchSpecific(t *testing.T) {
	series := seriesForYears(2019, 2023)
	first, err := series.BatchID()
	if err != nil || len(first) != 64 {
		t.Fatalf("batch id = %q, err=%v", first, err)
	}
	reordered := testSeries(append([]Period(nil), series.Periods...)...)
	reordered.Periods[0], reordered.Periods[1] = reordered.Periods[1], reordered.Periods[0]
	second, err := reordered.BatchID()
	if err != nil || first != second {
		t.Fatalf("period order changed batch id: %q vs %q, err=%v", first, second, err)
	}
	reordered.FetchedAt = reordered.FetchedAt.AddDate(1, 0, 0)
	third, err := reordered.BatchID()
	if err != nil || third == first {
		t.Fatalf("fetch time did not change batch id: %q vs %q, err=%v", first, third, err)
	}
}

func TestFundamentalDigestsCanonicalizeTimeZone(t *testing.T) {
	period := testPeriod(2023, number(10), number(100), nil)
	period.FetchedAt = time.Date(2024, 4, 21, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	shanghaiSeries := testSeries(period)
	utcPeriod := period
	utcPeriod.FetchedAt = period.FetchedAt.UTC()
	utcSeries := Series{SchemaVersion: Version, Code: testCode, Source: SourceTushare, FetchedAt: shanghaiSeries.FetchedAt.UTC(), Periods: []Period{utcPeriod}}
	firstDigest, err := period.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := utcPeriod.Digest()
	if err != nil || firstDigest != secondDigest {
		t.Fatalf("period digest depends on time zone: %q vs %q, err=%v", firstDigest, secondDigest, err)
	}
	firstBatch, err := shanghaiSeries.BatchID()
	if err != nil {
		t.Fatal(err)
	}
	secondBatch, err := utcSeries.BatchID()
	if err != nil || firstBatch != secondBatch {
		t.Fatalf("batch id depends on time zone: %q vs %q, err=%v", firstBatch, secondBatch, err)
	}
}

func TestSeriesRejectsFetchBeforePeriod(t *testing.T) {
	series := seriesForYears(2019, 2023)
	series.FetchedAt = series.Periods[0].FetchedAt.Add(-time.Second)
	if err := series.Validate(); err == nil {
		t.Fatal("series fetched before a period was accepted")
	}
}

func TestSeriesValidateRejectsDuplicatePeriod(t *testing.T) {
	period := testPeriod(2023, number(10), number(100), nil)
	series := testSeries(period, period)
	if err := series.Validate(); err == nil {
		t.Fatal("duplicate period was accepted")
	}
}

func TestPeriodValidateRejectsInvalidValues(t *testing.T) {
	valid := testPeriod(2023, number(10), number(100), nil)
	tests := []struct {
		name   string
		mutate func(*Period)
	}{
		{name: "report end not fiscal year", mutate: func(p *Period) { p.ReportEnd = "2023-12-30" }},
		{name: "available not after announcement", mutate: func(p *Period) { p.AvailableFrom = p.AnnouncementDate }},
		{name: "NaN profit", mutate: func(p *Period) { p.NetProfit = number(math.NaN()) }},
		{name: "Inf equity", mutate: func(p *Period) { p.Equity = number(math.Inf(1)) }},
		{name: "negative shares", mutate: func(p *Period) { p.TotalShares = number(-1) }},
		{name: "negative dividend", mutate: func(p *Period) { p.CashDividend = number(-1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			period := valid
			test.mutate(&period)
			if err := period.Validate(); err == nil {
				t.Fatal("invalid period was accepted")
			}
		})
	}
}
