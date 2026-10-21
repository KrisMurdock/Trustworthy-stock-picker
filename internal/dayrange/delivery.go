package dayrange

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/notify"
)

const deliveryLedgerPath = "state/dayrange-delivery.json"

// DeliveryStore is the atomic state surface needed for the delivery ledger.
// storage.Store implements it; tests use an in-memory implementation.
type DeliveryStore interface {
	Read(string, any) error
	Write(string, any) error
	Check() error
}

// ConfigureDelivery loads the persistent ledger and limits delivery to the
// predictor_dayrange route. Calling it again replaces the delivery dependencies
// without creating a second in-memory ledger.
func (s *Service) ConfigureDelivery(store DeliveryStore, channels []notify.Channel) error {
	if s == nil {
		return errors.New("dayrange service is incomplete")
	}
	if store == nil {
		return errors.New("dayrange delivery requires a state store")
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.deliveryStore = store
	s.noticeChannels = append([]notify.Channel(nil), channels...)
	var ledger notify.Ledger
	if err := store.Read(deliveryLedgerPath, &ledger); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		ledger = notify.NewLedger()
	}
	if ledger.Version != 2 {
		return errors.New("unsupported dayrange delivery ledger")
	}
	if err := ledger.Validate(); err != nil {
		return err
	}
	changed := ledger.Normalize(s.now())
	s.ledger = ledger
	if changed {
		return s.saveLedgerLocked()
	}
	return nil
}

// Flush delivers due premarket cards. Temporary send failures stay pending for
// a later monitor cycle; permanent failures remain blocked for inspection or an
// explicit retry through the shared ledger tooling.
func (s *Service) Flush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	return s.flushLocked(ctx, s.now())
}

func (s *Service) publishPremarket(ctx context.Context, target string, predictions []Prediction, skipped []string, generatedAt time.Time) error {
	if s == nil || !s.Config.DayRange.PushEnabled || len(predictions) == 0 {
		return nil
	}
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if s.deliveryStore == nil || len(s.deliveryChannelsLocked()) == 0 {
		return nil
	}
	names := make(map[string]string, len(s.Config.Stocks))
	for _, stock := range s.Config.Stocks {
		names[stock.Code] = stock.Name
	}
	cards, err := BuildPremarketCards(CardInput{
		TargetDate: target, GeneratedAt: generatedAt, PoolSize: len(s.Config.Stocks),
		Predictions: predictions, Names: names, Skipped: skipped,
	})
	if err != nil {
		return err
	}
	channelNames := s.deliveryChannelsLocked()
	previous := ""
	for index, card := range cards {
		dependsOn := ""
		if index > 0 {
			dependsOn = previous
		}
		part := fmt.Sprintf("page-%d", index+1)
		if err := s.ledger.EnqueuePart(target, card, channelNames, part, index, dependsOn); err != nil {
			return err
		}
		previous = card.EventID
	}
	if err := s.saveLedgerLocked(); err != nil {
		return err
	}
	return s.flushLocked(ctx, s.now())
}

func (s *Service) publishSavedPremarket(ctx context.Context, target string, records []PredictionRecord, skipped []string, now time.Time) error {
	if len(records) == 0 {
		return nil
	}
	predictions := make([]Prediction, 0, len(records))
	existing := make(map[string]bool, len(records))
	generatedAt := now
	for _, record := range records {
		predictions = append(predictions, record.Prediction)
		existing[record.Code] = true
		if !record.CreatedAt.IsZero() && (generatedAt.IsZero() || record.CreatedAt.Before(generatedAt)) {
			generatedAt = record.CreatedAt
		}
	}
	reported := make(map[string]bool, len(skipped))
	for _, item := range skipped {
		if index := strings.IndexByte(item, ':'); index > 0 {
			reported[item[:index]] = true
		}
	}
	for _, stock := range s.Config.Stocks {
		if !existing[stock.Code] && !reported[stock.Code] {
			skipped = append(skipped, stock.Code+":not_published")
		}
	}
	return s.publishPremarket(ctx, target, predictions, skipped, generatedAt)
}

func (s *Service) flushLocked(ctx context.Context, now time.Time) error {
	if s.deliveryStore == nil || len(s.ledger.Events) == 0 {
		return nil
	}
	if !s.Config.DayRange.PushEnabled {
		if s.ledger.Expire(now) {
			return s.saveLedgerLocked()
		}
		return nil
	}
	allowed := map[string]bool{}
	for _, name := range s.deliveryChannelsLocked() {
		allowed[name] = true
	}
	channels := map[string]notify.Channel{}
	for _, channel := range s.noticeChannels {
		if channel != nil && channel.Name() != "" && allowed[channel.Name()] {
			channels[channel.Name()] = channel
		}
	}
	for _, delivery := range s.ledger.Due(now) {
		channel, ok := channels[delivery.Channel]
		if !ok {
			_ = s.ledger.MarkFailure(delivery.EventID, delivery.Channel, &notify.DeliveryError{Kind: notify.Permanent, Reason: "channel_missing"}, now)
			continue
		}
		if !s.ledger.CanDeliver(delivery.EventID, delivery.Channel) {
			continue
		}
		if err := s.deliveryStore.Check(); err != nil {
			return err
		}
		if err := s.ledger.MarkInFlight(delivery.EventID, delivery.Channel, now); err != nil {
			continue
		}
		if err := s.saveLedgerLocked(); err != nil {
			return err
		}
		remaining := delivery.Message.ExpiresAt.Sub(s.now())
		if remaining <= 0 {
			s.ledger.Expire(s.now())
			continue
		}
		timeout := 10 * time.Second
		if remaining < timeout {
			timeout = remaining
		}
		sendCtx, cancel := context.WithTimeout(ctx, timeout)
		err := channel.Send(sendCtx, delivery.Message)
		cancel()
		if err == nil {
			_ = s.ledger.MarkDelivered(delivery.EventID, delivery.Channel)
		} else {
			failure := &notify.DeliveryError{Kind: notify.Retryable, Reason: "send_failed"}
			if classified, ok := err.(*notify.DeliveryError); ok {
				failure = classified
			}
			_ = s.ledger.MarkFailure(delivery.EventID, delivery.Channel, failure, s.now())
		}
		if err := s.saveLedgerLocked(); err != nil {
			return err
		}
	}
	s.ledger.Expire(now)
	s.ledger.Prune(now, 30*24*time.Hour)
	return s.saveLedgerLocked()
}

func (s *Service) deliveryChannelsLocked() []string {
	available := map[string]bool{}
	for _, channel := range s.noticeChannels {
		if channel != nil && channel.Name() != "" {
			available[channel.Name()] = true
		}
	}
	names := make([]string, 0)
	for _, name := range s.Config.ChannelNamesForRoute(config.NotifyRoutePredictorDayrange) {
		if available[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *Service) saveLedgerLocked() error {
	if s.deliveryStore == nil {
		return nil
	}
	if s.ledger.Version == 0 {
		s.ledger.Version = 2
	}
	if s.ledger.Events == nil {
		s.ledger.Events = map[string]notify.Event{}
	}
	return s.deliveryStore.Write(deliveryLedgerPath, s.ledger)
}
