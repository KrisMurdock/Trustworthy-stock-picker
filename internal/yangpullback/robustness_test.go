package yangpullback

import (
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
)

func TestTradesByYearAndUnionYears(t *testing.T) {
	trades := []Trade{
		{Signal: Signal{Date: "2020-01-02"}},
		{Signal: Signal{Date: "2021-05-06"}},
		{Signal: Signal{Date: "2020-12-31"}},
		{Signal: Signal{Date: ""}},
	}
	byYear := tradesByYear(trades)
	if len(byYear["2020"]) != 2 || len(byYear["2021"]) != 1 {
		t.Fatalf("by year = %+v", byYear)
	}
	years := unionYears(byYear, map[string][]Trade{"2019": nil})
	want := []string{"2019", "2020", "2021"}
	if len(years) != len(want) {
		t.Fatalf("years = %v", years)
	}
	for index := range want {
		if years[index] != want[index] {
			t.Fatalf("years = %v, want %v", years, want)
		}
	}
}

func TestAppendRobustnessBuildsOneToTenDayCurve(t *testing.T) {
	dates := testWeekdays(30)
	bars := make([]market.Bar, 0, len(dates))
	for _, date := range dates {
		bars = append(bars, market.Bar{Date: date, Open: 10, Close: 10, High: 10.5, Low: 9.5, Volume: 1000})
	}
	pair := testPair("sz000001", bars)
	pairs := map[string]pairedHistory{"sz000001": pair}
	patternSignals := []Signal{{Code: "sz000001", Date: dates[5]}}
	controlSignals := []Signal{{Code: "sz000001", Date: dates[6]}}
	opts := DefaultEvalOptions()
	opts.Bootstrap = 10
	result := Evaluation{Data: map[string]any{}}
	appendRobustness(&result, pairs, patternSignals, controlSignals, nil, nil, zeroCostStrategy(), testCalendar(), opts)
	if len(result.Robustness.Horizons) != 10 {
		t.Fatalf("horizons = %d", len(result.Robustness.Horizons))
	}
	for index, point := range result.Robustness.Horizons {
		if point.Horizon != index+1 {
			t.Fatalf("horizon[%d] = %d", index, point.Horizon)
		}
		if point.Pattern.Resolved != 1 || point.Control.Resolved != 1 {
			t.Fatalf("horizon %d stats = %+v", point.Horizon, point)
		}
	}
	if result.Data["robustness_scope"] == nil {
		t.Fatal("robustness scope was not recorded")
	}
}
