package strategy

import (
	"errors"
	"stock-watcher/internal/calendar"
	"stock-watcher/internal/config"
	"time"
)

// BacktestRange bounds SIGNAL dates inclusively. Entries and exits may occur
// later, so their entire horizon must be present before the saved as-of date.
type BacktestRange struct {
	From    string `json:"from"`
	Through string `json:"through"`
}

func (r *BacktestRange) Validate(c config.Config, asof string, horizon int) error {
	if r == nil {
		return nil
	}
	for _, v := range []string{r.From, r.Through} {
		if t, err := time.Parse("2006-01-02", v); err != nil || t.Format("2006-01-02") != v {
			return errors.New("backtest -from and -through require YYYY-MM-DD dates")
		}
	}
	if r.From > r.Through || r.From < c.Calendar.ValidFrom || r.Through > c.Calendar.ValidThrough || r.Through > asof {
		return errors.New("backtest signal range is reversed or outside the saved data/calendar")
	}
	cal := calendar.New(c)
	end := r.Through
	// Endpoints can be holidays; find the last signal session within the range.
	for {
		trading, err := cal.Trading(end)
		if err != nil {
			return err
		}
		if trading {
			break
		}
		t, _ := time.Parse("2006-01-02", end)
		end = t.AddDate(0, 0, -1).Format("2006-01-02")
		if end < r.From {
			return errors.New("backtest range contains no trading session")
		}
	}
	var err error
	for i := 0; i < horizon+1; i++ {
		end, err = cal.Next(end)
		if err != nil {
			return err
		}
	}
	if end > asof {
		return errors.New("saved data does not cover entry and exit after the requested signal range")
	}
	return nil
}

func (r *BacktestRange) Includes(date string) bool {
	return r == nil || date >= r.From && date <= r.Through
}
