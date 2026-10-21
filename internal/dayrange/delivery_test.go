package dayrange

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/feishu"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

type fakeDeliveryStore struct {
	raw    []byte
	exists bool
	writes int
}

func (f *fakeDeliveryStore) Read(_ string, out any) error {
	if !f.exists {
		return fs.ErrNotExist
	}
	return json.Unmarshal(f.raw, out)
}

func (f *fakeDeliveryStore) Write(_ string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f.raw = raw
	f.exists = true
	f.writes++
	return nil
}

func (f *fakeDeliveryStore) Check() error { return nil }

type fakeDayRangeChannel struct {
	name    string
	sent    []notify.Message
	failure error
}

func (f *fakeDayRangeChannel) Name() string { return f.name }

func (f *fakeDayRangeChannel) Send(_ context.Context, message notify.Message) error {
	f.sent = append(f.sent, message)
	return f.failure
}

func deliveryServiceConfig() config.Config {
	c := serviceConfig()
	c.DayRange.PushEnabled = true
	c.Feishu.Enabled = false
	c.Notify.Channels = []config.NotifyChannel{
		{Name: "selection-bot", Type: "feishu", Enabled: true, Route: config.NotifyRouteSelection},
		{Name: "predictor-dayrange", Type: "feishu", Enabled: true, Route: config.NotifyRoutePredictorDayrange},
	}
	return c
}

func cardPrediction(index int) Prediction {
	code := fmt.Sprintf("sz%06d", index+1)
	reference := 10 + float64(index%20)/10
	return Prediction{
		PredictionID: fmt.Sprintf("prediction-%d", index), Code: code, TargetDate: "2026-10-21", Mode: ModePremarket,
		AsOf: time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai), InputThrough: "2026-10-20", ModelID: "tree-model",
		Source: "model", ReferencePrice: reference, PointHigh: reference * 1.03, PointLow: reference * .98,
		IntervalHigh: &Interval{Lower: reference * 1.01, Upper: reference * 1.05, NominalCoverage: .8, Method: "conformal"},
		IntervalLow:  &Interval{Lower: reference * .96, Upper: reference * .995, NominalCoverage: .8, Method: "conformal"},
	}
}

func TestBuildPremarketCardsPaginatesAndFitsFeishuPayload(t *testing.T) {
	predictions := make([]Prediction, 0, 500)
	names := map[string]string{}
	for i := 0; i < 500; i++ {
		prediction := cardPrediction(i)
		predictions = append(predictions, prediction)
		names[prediction.Code] = strings.Repeat("上海超长股票名称", 3)
	}
	cards, err := BuildPremarketCards(CardInput{
		TargetDate: "2026-10-21", GeneratedAt: time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai),
		PoolSize: 520, Predictions: predictions, Names: names, Skipped: []string{"sz999999:missing_reference"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) < 2 {
		t.Fatalf("cards=%d want multiple pages", len(cards))
	}
	rows := 0
	for index, card := range cards {
		expectedID := fmt.Sprintf("dayrange:premarket:2026-10-21:%d", index+1)
		if card.EventID != expectedID {
			t.Fatalf("page %d event id=%q want %q", index+1, card.EventID, expectedID)
		}
		if !card.ExpiresAt.Equal(time.Date(2026, 10, 21, 9, 30, 0, 0, shanghai)) {
			t.Fatalf("page %d expiry=%s", index+1, card.ExpiresAt)
		}
		if !strings.Contains(card.Title, fmt.Sprintf("（%d/%d）", index+1, len(cards))) {
			t.Fatalf("page %d title=%q", index+1, card.Title)
		}
		if len(card.Sections) < 2 || card.Sections[1].Table == nil {
			t.Fatalf("page %d has no table", index+1)
		}
		if len(card.Sections[1].Table.Rows) > premarketCardMaxRows {
			t.Fatalf("page %d rows=%d exceeds cap", index+1, len(card.Sections[1].Table.Rows))
		}
		rows += len(card.Sections[1].Table.Rows)
		payload, err := json.Marshal(feishu.Card(card))
		if err != nil {
			t.Fatal(err)
		}
		if len(payload) > 20*1024 {
			t.Fatalf("page %d payload=%d exceeds Feishu limit", index+1, len(payload))
		}
	}
	if rows != len(predictions) {
		t.Fatalf("table rows=%d want %d", rows, len(predictions))
	}
	if !strings.Contains(fmt.Sprint(cards[0].Sections[0].Fields), "500/520") {
		t.Fatalf("coverage missing from card: %+v", cards[0].Sections[0].Fields)
	}
	lines := strings.Join(cards[0].Sections[1].Lines, " ")
	if !strings.Contains(lines, "missing_reference 1") || !strings.Contains(lines, "不构成投资建议") {
		t.Fatalf("card caveats or skipped reasons missing: %q", lines)
	}
}

func TestPremarketDeliveryRoutesOnlyToDayrangeAndIsIdempotent(t *testing.T) {
	c := deliveryServiceConfig()
	store := &fakeDeliveryStore{}
	selection := &fakeDayRangeChannel{name: "selection-bot"}
	dayrangeChannel := &fakeDayRangeChannel{name: "predictor-dayrange"}
	service := NewService(c, newFakeDayRangeStore(), fakeDayRangeMarket{}, nil)
	service.Now = func() time.Time { return time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai) }
	if err := service.ConfigureDelivery(store, []notify.Channel{selection, dayrangeChannel}); err != nil {
		t.Fatal(err)
	}
	predictions := []Prediction{cardPrediction(1), cardPrediction(2)}
	if err := service.publishPremarket(context.Background(), "2026-10-21", predictions, nil, service.Now()); err != nil {
		t.Fatal(err)
	}
	if len(dayrangeChannel.sent) != 1 || len(selection.sent) != 0 {
		t.Fatalf("route isolation failed: dayrange=%d selection=%d", len(dayrangeChannel.sent), len(selection.sent))
	}
	if len(service.ledger.Events) != 1 {
		t.Fatalf("ledger events=%d want 1", len(service.ledger.Events))
	}
	if err := service.publishPremarket(context.Background(), "2026-10-21", predictions, nil, service.Now()); err != nil {
		t.Fatal(err)
	}
	if len(dayrangeChannel.sent) != 1 || len(service.ledger.Events) != 1 {
		t.Fatalf("duplicate delivery: sent=%d events=%d", len(dayrangeChannel.sent), len(service.ledger.Events))
	}
	var persisted notify.Ledger
	if err := store.Read(deliveryLedgerPath, &persisted); err != nil {
		t.Fatal(err)
	}
	if err := persisted.Validate(); err != nil {
		t.Fatalf("persisted ledger is invalid: %v", err)
	}
}

func TestPremarketDeliveryRetriesAfterRestart(t *testing.T) {
	c := deliveryServiceConfig()
	store := &fakeDeliveryStore{}
	now := time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai)
	failing := &fakeDayRangeChannel{name: "predictor-dayrange", failure: &notify.DeliveryError{Kind: notify.Retryable, Reason: "network"}}
	first := NewService(c, newFakeDayRangeStore(), fakeDayRangeMarket{}, nil)
	first.Now = func() time.Time { return now }
	if err := first.ConfigureDelivery(store, []notify.Channel{failing}); err != nil {
		t.Fatal(err)
	}
	if err := first.publishPremarket(context.Background(), "2026-10-21", []Prediction{cardPrediction(1)}, nil, now); err != nil {
		t.Fatal(err)
	}
	if len(failing.sent) != 1 {
		t.Fatalf("send attempts=%d want 1", len(failing.sent))
	}
	if event := first.ledger.Events["dayrange:premarket:2026-10-21:1"]; event.Channels["predictor-dayrange"].Status != notify.Pending {
		t.Fatalf("retryable event status=%+v", event)
	}

	success := &fakeDayRangeChannel{name: "predictor-dayrange"}
	second := NewService(c, newFakeDayRangeStore(), fakeDayRangeMarket{}, nil)
	second.Now = func() time.Time { return now.Add(3 * time.Minute) }
	if err := second.ConfigureDelivery(store, []notify.Channel{success}); err != nil {
		t.Fatal(err)
	}
	if err := second.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(success.sent) != 1 {
		t.Fatalf("restart flush sent=%d want 1", len(success.sent))
	}
	if event := second.ledger.Events["dayrange:premarket:2026-10-21:1"]; event.Channels["predictor-dayrange"].Status != notify.Delivered {
		t.Fatalf("retried event status=%+v", event)
	}
}

func TestPremarketDeliveryDisabledDoesNotEnqueue(t *testing.T) {
	c := deliveryServiceConfig()
	c.DayRange.PushEnabled = false
	store := &fakeDeliveryStore{}
	channel := &fakeDayRangeChannel{name: "predictor-dayrange"}
	service := NewService(c, newFakeDayRangeStore(), fakeDayRangeMarket{}, nil)
	service.Now = func() time.Time { return time.Date(2026, 10, 21, 8, 45, 0, 0, shanghai) }
	if err := service.ConfigureDelivery(store, []notify.Channel{channel}); err != nil {
		t.Fatal(err)
	}
	if err := service.publishPremarket(context.Background(), "2026-10-21", []Prediction{cardPrediction(1)}, nil, service.Now()); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 0 || len(service.ledger.Events) != 0 || store.writes != 0 {
		t.Fatalf("disabled push sent=%d events=%d writes=%d", len(channel.sent), len(service.ledger.Events), store.writes)
	}
}

func TestPredictQueuesSavedPredictionsOnce(t *testing.T) {
	c := deliveryServiceConfig()
	days := serviceDays(c, "2025-09-01", "2026-10-20")
	now := time.Date(2026, 10, 20, 8, 45, 0, 0, shanghai)
	store := newFakeDayRangeStore()
	service := NewService(c, store, fakeDayRangeMarket{data: premarketData(days, "sz000001")}, nil)
	service.Now = func() time.Time { return now }
	service.BuildVersion = "test"
	state := &fakeDeliveryStore{}
	channel := &fakeDayRangeChannel{name: "predictor-dayrange"}
	if err := service.ConfigureDelivery(state, []notify.Channel{channel}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Predict(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Predictions) != 1 || len(channel.sent) != 1 {
		t.Fatalf("predict result=%+v sent=%d", result, len(channel.sent))
	}
	if _, err := service.Predict(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if len(channel.sent) != 1 || len(service.ledger.Events) != 1 {
		t.Fatalf("idempotent predict sent=%d events=%d", len(channel.sent), len(service.ledger.Events))
	}
	event := service.ledger.Events["dayrange:premarket:2026-10-20:1"]
	if event.Channels["predictor-dayrange"].Status != notify.Delivered {
		t.Fatalf("prediction delivery status=%+v", event.Channels)
	}
	if event.Message.Sections[1].Table == nil || len(event.Message.Sections[1].Table.Rows) != 1 {
		t.Fatalf("prediction card rows=%+v", event.Message.Sections)
	}
}

func TestConfigureDeliveryRejectsCorruptLedger(t *testing.T) {
	store := &fakeDeliveryStore{exists: true, raw: []byte(`{"schema_version":2,"events":{"bad":{}}}`)}
	service := NewService(deliveryServiceConfig(), newFakeDayRangeStore(), fakeDayRangeMarket{}, nil)
	err := service.ConfigureDelivery(store, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid persisted delivery ledger") {
		t.Fatalf("corrupt ledger accepted: %v", err)
	}
}
