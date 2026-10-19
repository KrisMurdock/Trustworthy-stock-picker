package intraday

import (
	"context"
	"errors"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func moment(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, market.Shanghai)
	if err != nil {
		panic(err)
	}
	return t
}
func settings() config.Config {
	c := config.Defaults()
	c.RequireCIFS = false
	c.Calendar.ValidFrom = "2026-01-01"
	c.Calendar.ValidThrough = "2026-12-31"
	c.Calendar.Holidays = []string{"2026-10-01"}
	c.Stocks = []config.Stock{{Code: "sz000001", Name: "测试"}, {Code: "sh600519", Name: "测试二"}}
	return c
}
func TestScheduleSessions(t *testing.T) {
	c := calendar.New(settings())
	for _, tc := range []struct {
		now, target         string
		active, calibration bool
	}{
		{"2026-10-15 09:29:59", "2026-10-14 15:00:00", false, true},
		{"2026-10-15 09:30:00", "2026-10-15 09:35:00", true, false},
		{"2026-10-15 10:04:59", "2026-10-15 10:05:00", true, false},
		{"2026-10-15 11:30:00", "2026-10-15 11:30:00", false, false},
		{"2026-10-15 12:59:59", "2026-10-15 11:30:00", false, false},
		{"2026-10-15 13:00:00", "2026-10-15 13:05:00", true, false},
		{"2026-10-15 15:00:00", "2026-10-15 15:00:00", false, false},
		{"2026-10-15 15:10:00", "2026-10-15 15:00:00", false, true},
		{"2026-10-17 10:00:00", "2026-10-16 15:00:00", false, true},
		{"2026-10-01 13:00:00", "2026-09-30 15:00:00", false, true},
	} {
		p, e := Schedule(c, moment(tc.now), "15:10")
		if e != nil || !p.Target.Equal(moment(tc.target)) || p.Active != tc.active || p.Calibration != tc.calibration {
			t.Fatalf("%s: %+v %v", tc.now, p, e)
		}
	}
	if _, e := Schedule(c, moment("2027-01-04 10:00:00"), "15:10"); e == nil {
		t.Fatal("expired calendar accepted")
	}
}

type sourceFunc func(context.Context, config.Stock, int) (market.MinuteHistory, error)

func (f sourceFunc) FetchMinutes(ctx context.Context, s config.Stock, n int) (market.MinuteHistory, error) {
	return f(ctx, s, n)
}
func sample(c config.Config, s config.Stock, now time.Time) market.MinuteHistory {
	p, e := Schedule(calendar.New(c), now, c.Intraday.CalibrationAt)
	if e != nil {
		panic(e)
	}
	h := market.MinuteHistory{Version: 1, Code: s.Code, Name: s.Name, Source: "tencent", Period: 5, BasePeriod: 5, Adjust: "none", VolumeUnit: "provider_raw", FetchedAt: now}
	day := moment(calendar.Date(p.Target) + " 00:00:00")
	for _, start := range []int{570, 780} {
		for n := start + 5; n <= start+120; n += 5 {
			end := day.Add(time.Duration(n) * time.Minute)
			if end.After(p.Target) {
				break
			}
			h.Bars = append(h.Bars, market.MinuteBar{End: end, Open: 10, High: 12, Low: 9, Close: 11, Volume: 100, Final: !end.After(now.Add(-10 * time.Second))})
		}
	}
	return h
}
func setup(t *testing.T, c config.Config, now *time.Time, f sourceFunc) *Monitor {
	t.Helper()
	root := t.TempDir()
	s, e := storage.Open(root, false)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	db, e := storage.InitMarket(root, false)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	c.DataDir = root
	m := New(c, s, db, f, nil)
	m.Now = func() time.Time { return *now }
	return m
}
func TestSyncFormingIsolationCalibrationAndRestart(t *testing.T) {
	ctx := context.Background()
	c := settings()
	now := moment("2026-10-15 10:01:00")
	failSecond := true
	closePrice := 11.0
	var calls atomic.Int32
	m := setup(t, c, &now, func(ctx context.Context, s config.Stock, n int) (market.MinuteHistory, error) {
		calls.Add(1)
		if n != 64 && n != 640 {
			t.Errorf("unexpected fetch count %d", n)
		}
		if failSecond && s.Code == "sh600519" {
			return market.MinuteHistory{}, &market.Failure{Status: 403}
		}
		h := sample(c, s, now)
		h.Bars[len(h.Bars)-1].Close = closePrice
		return h, nil
	})
	r, e := m.Sync(ctx, now)
	if e != nil || r.Succeeded != 1 || r.Failed != 1 {
		t.Fatalf("isolation: %+v %v", r, e)
	}
	snap, e := m.DB.LoadMinutes(ctx, c.Stocks[0], 15)
	if e != nil || len(snap.Bars) != 3 || snap.Bars[2].Final || snap.Bars[2].End.Format("15:04") != "10:15" {
		t.Fatalf("forming: %+v %v", snap, e)
	}
	if r, e = m.Sync(ctx, now); e != nil || r.Processed != 0 {
		t.Fatal("poll interval ignored", r, e)
	}
	now = now.Add(time.Minute)
	closePrice = 11.5
	failSecond = false
	if r, e = m.Sync(ctx, now); e != nil || r.Succeeded != 2 {
		t.Fatal(r, e)
	}
	snap, e = m.DB.LoadMinutes(ctx, c.Stocks[0], 5)
	if e != nil || len(snap.Bars) != 7 || snap.Bars[6].Close != 11.5 || snap.Bars[6].Volume != 100 {
		t.Fatal("duplicate/correction", snap, e)
	}
	now = moment("2026-10-15 11:31:00")
	if r, e = m.Sync(ctx, now); e != nil || r.Succeeded != 2 {
		t.Fatal(r, e)
	}
	now = moment("2026-10-15 12:00:00")
	if r, e = m.Sync(ctx, now); e != nil || r.Processed != 0 {
		t.Fatal("lunch polling", r, e)
	}
	now = moment("2026-10-15 15:10:00")
	if r, e = m.Sync(ctx, now); e != nil || r.Calibrated != 2 {
		t.Fatal("calibration", r, e)
	}
	snap, e = m.DB.LoadMinutes(ctx, c.Stocks[0], 60)
	if e != nil || len(snap.Bars) != 4 || !snap.Bars[3].Final || snap.Bars[3].Close != 11.5 {
		t.Fatal("post-close aggregation", snap, e)
	}
	m.DB.Close()
	db, e := storage.OpenMarket(m.Config.DataDir, false)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	m.DB = db
	now = now.Add(time.Hour)
	before := calls.Load()
	if r, e = m.Sync(ctx, now); e != nil || r.Processed != 0 || calls.Load() != before {
		t.Fatal("calibration not durable", r, e)
	}
}
func TestIncompleteAndStaleDataAreRetainedButNotCalibrated(t *testing.T) {
	c := settings()
	c.Stocks = c.Stocks[:1]
	now := moment("2026-10-15 15:10:00")
	mode := "gap"
	m := setup(t, c, &now, func(_ context.Context, s config.Stock, _ int) (market.MinuteHistory, error) {
		h := sample(c, s, now)
		switch mode {
		case "gap":
			h.Bars = append(h.Bars[:6], h.Bars[7:]...)
		case "stale":
			h.Bars = h.Bars[:47]
		}
		return h, nil
	})
	for _, want := range []string{"partial", "stale", "ok"} {
		r, e := m.Sync(context.Background(), now)
		s, se := m.DB.LoadMinutes(context.Background(), c.Stocks[0], 5)
		if e != nil || se != nil || s.State.Status != want || len(s.Bars) == 0 {
			t.Fatal(r, s, e, se)
		}
		if want != "ok" && r.Calibrated != 0 {
			t.Fatal("incomplete calibration", r)
		}
		now = now.Add(5 * time.Minute)
		if mode == "gap" {
			mode = "stale"
		} else {
			mode = "full"
		}
	}
}
func TestCancellationWorkersAndStorageGuard(t *testing.T) {
	c := settings()
	c.Intraday.Workers = 1
	now := moment("2026-10-15 10:01:00")
	started := make(chan struct{})
	var calls atomic.Int32
	m := setup(t, c, &now, func(ctx context.Context, _ config.Stock, _ int) (market.MinuteHistory, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return market.MinuteHistory{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := m.Sync(ctx, now); done <- e }()
	<-started
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) || calls.Load() != 1 {
			t.Fatal(e, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation leaked worker")
	}
	if e := os.Rename(m.Config.DataDir, m.Config.DataDir+"-away"); e != nil {
		t.Fatal(e)
	}
	defer os.Rename(m.Config.DataDir+"-away", m.Config.DataDir)
	if _, e := m.Sync(context.Background(), now); e == nil || calls.Load() != 1 {
		t.Fatal("source called after storage disappeared", e)
	}
}
