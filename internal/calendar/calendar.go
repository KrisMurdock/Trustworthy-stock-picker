package calendar

import (
	"errors"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"time"
)

type Calendar struct {
	start, end string
	closed     map[string]bool
	days       map[string]bool
	source     string
	digest     string
}

func New(c config.Config) *Calendar {
	x := &Calendar{start: c.Calendar.ValidFrom, end: c.Calendar.ValidThrough, closed: map[string]bool{}, source: c.Calendar.Source, digest: c.Calendar.Digest}
	if x.source == "" {
		x.source = "configured"
	}
	for _, d := range c.Calendar.Holidays {
		x.closed[d] = true
	}
	if len(c.Calendar.TradingDays) > 0 {
		x.days = make(map[string]bool, len(c.Calendar.TradingDays))
		for _, day := range c.Calendar.TradingDays {
			x.days[day] = true
		}
	}
	return x
}

func (c *Calendar) ValidFrom() string {
	return c.start
}

func (c *Calendar) Source() string { return c.source }

func (c *Calendar) Digest() string { return c.digest }

// Covers reports whether the calendar bounds include the requested range. A
// frozen calendar is trusted only inside its declared provider window.
func (c *Calendar) Covers(start, end string) (bool, error) {
	if err := ValidateRange(start, end); err != nil {
		return false, err
	}
	return start >= c.start && end <= c.end, nil
}

func Date(t time.Time) string { return t.In(market.Shanghai).Format("2006-01-02") }
func (c *Calendar) Trading(day string) (bool, error) {
	t, err := time.ParseInLocation("2006-01-02", day, market.Shanghai)
	if err != nil || day < c.start || day > c.end {
		return false, errors.New("date outside configured exchange calendar coverage")
	}
	if c.days != nil {
		return c.days[day], nil
	}
	return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday && !c.closed[day], nil
}

func (c *Calendar) TradingDays(start, end string) ([]string, error) {
	first, err := time.ParseInLocation("2006-01-02", start, market.Shanghai)
	if err != nil {
		return nil, errors.New("invalid start date")
	}
	last, err := time.ParseInLocation("2006-01-02", end, market.Shanghai)
	if err != nil || first.After(last) {
		return nil, errors.New("invalid end date")
	}
	if start < c.start || end > c.end {
		return nil, errors.New("date outside configured exchange calendar coverage")
	}
	days := []string{}
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		date := Date(day)
		ok, err := c.Trading(date)
		if err != nil {
			return nil, err
		}
		if ok {
			days = append(days, date)
		}
	}
	return days, nil
}
func (c *Calendar) Next(day string) (string, error) {
	t, err := time.ParseInLocation("2006-01-02", day, market.Shanghai)
	if err != nil {
		return "", err
	}
	for i := 0; i < 31; i++ {
		t = t.AddDate(0, 0, 1)
		date := Date(t)
		ok, err := c.Trading(date)
		if err != nil {
			return "", err
		}
		if ok {
			return date, nil
		}
	}
	return "", errors.New("no next trading day in calendar")
}
func (c *Calendar) Completed(now time.Time) (string, error) {
	t := now.In(market.Shanghai)
	day := Date(t)
	if _, err := c.Trading(day); err != nil {
		return "", err
	}
	if t.Hour() < 15 {
		t = t.AddDate(0, 0, -1)
	}
	for i := 0; i < 31; i++ {
		day = Date(t)
		ok, err := c.Trading(day)
		if err != nil {
			return "", err
		}
		if ok {
			return day, nil
		}
		t = t.AddDate(0, 0, -1)
	}
	return "", errors.New("no completed trading day in calendar")
}

// Due returns the most recent complete session only after its configured publication time.
func (c *Calendar) Due(now time.Time, runAt string) (string, string, bool, error) {
	asof, err := c.Completed(now)
	if err != nil {
		return "", "", false, err
	}
	clock, err := time.Parse("15:04", runAt)
	if err != nil {
		return "", "", false, err
	}
	d, _ := time.ParseInLocation("2006-01-02", asof, market.Shanghai)
	ready := d.Add(time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute)
	if now.Before(ready) {
		return asof, "", false, nil
	}
	target, err := c.Next(asof)
	if err != nil {
		return "", "", false, err
	}
	// A delayed T+1 recommendation expires when T+1 opens, never catch up after that.
	open, _ := time.ParseInLocation("2006-01-02 15:04", target+" 09:30", market.Shanghai)
	return asof, target, now.Before(open), nil
}
func Expired(target string, now time.Time) bool {
	t, err := time.ParseInLocation("2006-01-02 15:04", target+" 09:30", market.Shanghai)
	return err != nil || !now.Before(t)
}
