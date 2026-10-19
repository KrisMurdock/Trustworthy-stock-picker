package strategy

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"testing"
)

func TestPriceLimitForUsesTradingDay(t *testing.T) {
	tests := []struct {
		code string
		day  string
		want float64
	}{
		{"sh600000", "2026-10-19", 10},
		{"sz000001", "2026-10-19", 10},
		{"sh688981", "2026-10-19", 20},
		{"sz300001", "2020-08-21", 10},
		{"sz300001", "2020-08-24", 20},
		{"sz300001", "2026-10-19", 20},
	}
	for _, tt := range tests {
		rule := PriceLimitFor(tt.code, tt.day)
		if !rule.Known || rule.Pct != tt.want {
			t.Fatalf("%s on %s: got %+v, want %.0f%%", tt.code, tt.day, rule, tt.want)
		}
		if rule.STStatus != STStatusUnknown {
			t.Fatalf("%s on %s: historical ST status was guessed: %v", tt.code, tt.day, rule.STStatus)
		}
	}
}

func TestPriceLimitForUnknownInputs(t *testing.T) {
	tests := []struct {
		code string
		day  string
	}{
		{"bj430047", "2026-10-19"},
		{"sh500000", "2026-10-19"},
		{"sh600000", "2020-02-30"},
		{"sh600000", ""},
	}
	for _, tt := range tests {
		if rule := PriceLimitFor(tt.code, tt.day); rule.Known {
			t.Fatalf("%s on %q: unexpectedly known: %+v", tt.code, tt.day, rule)
		}
	}
}

func TestLockedDownUsesTradingDayRule(t *testing.T) {
	const previous = 10.0
	bar := market.Bar{Date: "2020-08-21", Open: 9, High: 9, Low: 9, Close: 9, Volume: 100}
	if !lockedDown(bar, previous, "sz300001") {
		t.Fatal("pre-reform ChiNext limit-down was not detected")
	}
	bar.Date = "2020-08-24"
	if lockedDown(bar, previous, "sz300001") {
		t.Fatal("post-reform ChiNext used the old 10% limit")
	}
	bar.Open, bar.High, bar.Low, bar.Close = 8, 8, 8, 8
	if !lockedDown(bar, previous, "sz300001") {
		t.Fatal("post-reform ChiNext limit-down was not detected")
	}

	undated := market.Bar{Open: 9, High: 9, Low: 9, Close: 9, Volume: 100}
	if !lockedDown(undated, previous, "sz300001") {
		t.Fatal("undated synthetic bar did not use the conservative 10% default")
	}
}
