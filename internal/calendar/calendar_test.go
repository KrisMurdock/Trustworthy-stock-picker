package calendar

import (
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"testing"
	"time"
)

func TestNextAndCompleted(t *testing.T) {
	cfg := config.Defaults()
	cfg.Calendar.ValidFrom = "2026-01-01"
	cfg.Calendar.ValidThrough = "2026-12-31"
	cfg.Calendar.Holidays = []string{"2026-10-01", "2026-10-02", "2026-10-05", "2026-10-06", "2026-10-07"}
	c := New(cfg)
	for _, tt := range []struct{ day, want string }{{"2026-10-09", "2026-10-12"}, {"2026-09-30", "2026-10-08"}} {
		got, err := c.Next(tt.day)
		if err != nil || got != tt.want {
			t.Fatalf("%s -> %s (%v)", tt.day, got, err)
		}
	}
	for _, tt := range []struct{ now, want string }{{"2026-10-14 14:59", "2026-10-13"}, {"2026-10-14 15:00", "2026-10-14"}, {"2026-10-03 18:00", "2026-09-30"}} {
		now, _ := time.ParseInLocation("2006-01-02 15:04", tt.now, market.Shanghai)
		got, err := c.Completed(now)
		if err != nil || got != tt.want {
			t.Fatalf("%s -> %s (%v)", tt.now, got, err)
		}
	}
	if _, err := c.Next("2026-12-31"); err == nil {
		t.Fatal("accepted uncovered T+1 calendar")
	}
	now, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-14 17:00", market.Shanghai)
	_, _, due, err := c.Due(now, "18:05")
	if err != nil || due {
		t.Fatal("premature publication")
	}
	now = now.Add(2 * time.Hour)
	asof, next, due, err := c.Due(now, "18:05")
	if err != nil || !due || asof != "2026-10-14" || next != "2026-10-15" {
		t.Fatal("missing publication")
	}
}

func TestTradingDaysRejectsUncoveredWindow(t *testing.T) {
	cfg := config.Defaults()
	cfg.Calendar.ValidFrom = "2026-01-01"
	cfg.Calendar.ValidThrough = "2026-12-31"
	c := New(cfg)
	if _, err := c.TradingDays("2025-12-31", "2026-01-05"); err == nil {
		t.Fatal("accepted start outside calendar coverage")
	}
	if _, err := c.TradingDays("2026-12-30", "2027-01-02"); err == nil {
		t.Fatal("accepted end outside calendar coverage")
	}
}
