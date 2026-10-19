package notify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

type Message struct {
	Version   int       `json:"version"`
	EventID   string    `json:"event_id"`
	Title     string    `json:"title"`
	Sections  []Section `json:"sections"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Section struct {
	Heading  string   `json:"heading,omitempty"`
	Lines    []string `json:"lines"`
	Fields   []Field  `json:"fields,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

type Kind string

const (
	Retryable Kind = "retryable"
	Permanent Kind = "permanent"
	Unknown   Kind = "unknown"
)

type DeliveryError struct {
	Channel    string        `json:"-"`
	Kind       Kind          `json:"kind"`
	Status     int           `json:"status,omitempty"`
	RetryAfter time.Duration `json:"-"`
	Reason     string        `json:"reason"`
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return "delivery failed"
	}
	if e.Status > 0 {
		return fmt.Sprintf("delivery failed: %s (HTTP %d)", e.Reason, e.Status)
	}
	return "delivery failed: " + e.Reason
}

func (e *DeliveryError) Retryable() bool {
	return e != nil && e.Kind != Permanent
}

type Channel interface {
	Name() string
	Send(context.Context, Message) error
}

type Status string

const (
	Pending   Status = "pending"
	InFlight  Status = "in_flight"
	Delivered Status = "delivered"
	Blocked   Status = "blocked"
	Expired   Status = "expired"
	Unknowned Status = "unknown"
)

type ChannelState struct {
	Status            Status    `json:"status"`
	Kind              Kind      `json:"kind,omitempty"`
	Attempts          int       `json:"attempts"`
	NextAttempt       time.Time `json:"next_attempt,omitempty"`
	InFlightAt        time.Time `json:"in_flight_at,omitempty"`
	PossiblyDuplicate bool      `json:"possibly_duplicate,omitempty"`
}

type Event struct {
	AsOf      string                  `json:"as_of"`
	ExpiresAt time.Time               `json:"expires_at"`
	Message   Message                 `json:"message"`
	Channels  map[string]ChannelState `json:"channels"`
}

type Ledger struct {
	Version int              `json:"schema_version"`
	Events  map[string]Event `json:"events"`
}

type Delivery struct {
	EventID string
	Channel string
	Message Message
}

func NewLedger() Ledger {
	return Ledger{Version: 2, Events: map[string]Event{}}
}

func (l *Ledger) Normalize(now time.Time) bool {
	if l.Events == nil {
		l.Events = map[string]Event{}
	}
	changed := false
	for eventID, event := range l.Events {
		if event.Channels == nil {
			event.Channels = map[string]ChannelState{}
			changed = true
		}
		for channel, state := range event.Channels {
			if state.Status == InFlight {
				state.Status = Unknowned
				state.Kind = Unknown
				state.InFlightAt = time.Time{}
				state.PossiblyDuplicate = true
				event.Channels[channel] = state
				changed = true
			}
		}
		l.Events[eventID] = event
	}
	return changed
}

func (l *Ledger) Enqueue(asof string, message Message, channels []string) error {
	if l.Version == 0 {
		l.Version = 2
	}
	if l.Version != 2 {
		return errors.New("unsupported delivery ledger version")
	}
	if !supportedMessageVersion(message.Version) || message.EventID == "" || asof == "" || message.ExpiresAt.IsZero() {
		return errors.New("invalid notification message")
	}
	if l.Events == nil {
		l.Events = map[string]Event{}
	}
	event, exists := l.Events[message.EventID]
	if !exists {
		event = Event{
			AsOf:      asof,
			ExpiresAt: message.ExpiresAt,
			Message:   message,
			Channels:  map[string]ChannelState{},
		}
	} else if event.AsOf != asof || event.Message.EventID != message.EventID {
		return errors.New("notification event changed")
	}
	for _, channel := range channels {
		if channel == "" {
			return errors.New("notification channel name is required")
		}
		if _, exists := event.Channels[channel]; !exists {
			event.Channels[channel] = ChannelState{Status: Pending}
		}
	}
	l.Events[message.EventID] = event
	return nil
}

func (l *Ledger) Expire(now time.Time) bool {
	changed := false
	for eventID, event := range l.Events {
		if event.ExpiresAt.IsZero() || now.Before(event.ExpiresAt) {
			continue
		}
		for channel, state := range event.Channels {
			if state.Status == Delivered || state.Status == Expired {
				continue
			}
			state.Status = Expired
			state.Kind = ""
			state.NextAttempt = time.Time{}
			state.InFlightAt = time.Time{}
			event.Channels[channel] = state
			changed = true
		}
		l.Events[eventID] = event
	}
	return changed
}

func (l *Ledger) Prune(now time.Time, keep time.Duration) bool {
	changed := false
	for eventID, event := range l.Events {
		if event.ExpiresAt.IsZero() || now.Before(event.ExpiresAt.Add(keep)) {
			continue
		}
		terminal := true
		for _, state := range event.Channels {
			switch state.Status {
			case Delivered, Blocked, Expired:
			default:
				terminal = false
			}
		}
		if terminal {
			delete(l.Events, eventID)
			changed = true
		}
	}
	return changed
}

func (l Ledger) Due(now time.Time) []Delivery {
	eventIDs := make([]string, 0, len(l.Events))
	for eventID := range l.Events {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Strings(eventIDs)
	out := []Delivery{}
	for _, eventID := range eventIDs {
		event := l.Events[eventID]
		if !now.Before(event.ExpiresAt) {
			continue
		}
		channels := make([]string, 0, len(event.Channels))
		for channel := range event.Channels {
			channels = append(channels, channel)
		}
		sort.Strings(channels)
		for _, channel := range channels {
			state := event.Channels[channel]
			if (state.Status != Pending && state.Status != Unknowned) || now.Before(state.NextAttempt) {
				continue
			}
			out = append(out, Delivery{EventID: eventID, Channel: channel, Message: event.Message})
		}
	}
	return out
}

func (l *Ledger) MarkInFlight(eventID, channel string, now time.Time) error {
	state, err := l.channelState(eventID, channel)
	if err != nil {
		return err
	}
	state.Status = InFlight
	state.InFlightAt = now
	l.Events[eventID].Channels[channel] = state
	return nil
}

func (l *Ledger) MarkDelivered(eventID, channel string) error {
	state, err := l.channelState(eventID, channel)
	if err != nil {
		return err
	}
	state.Status = Delivered
	state.Kind = ""
	state.NextAttempt = time.Time{}
	state.InFlightAt = time.Time{}
	l.Events[eventID].Channels[channel] = state
	return nil
}

func (l *Ledger) MarkFailure(eventID, channel string, failure *DeliveryError, now time.Time) error {
	state, err := l.channelState(eventID, channel)
	if err != nil {
		return err
	}
	state.Attempts++
	state.Kind = failure.Kind
	state.InFlightAt = time.Time{}
	if failure.Kind == Permanent {
		state.Status = Blocked
		state.NextAttempt = time.Time{}
	} else {
		state.Status = Pending
		delay := time.Duration(1<<min(state.Attempts, 8)) * time.Minute
		if failure.RetryAfter > delay {
			delay = failure.RetryAfter
		}
		state.NextAttempt = now.Add(delay)
	}
	l.Events[eventID].Channels[channel] = state
	return nil
}

func (l *Ledger) MarkUnknown(eventID, channel string) error {
	state, err := l.channelState(eventID, channel)
	if err != nil {
		return err
	}
	state.Status = Unknowned
	state.Kind = Unknown
	state.InFlightAt = time.Time{}
	state.PossiblyDuplicate = true
	l.Events[eventID].Channels[channel] = state
	return nil
}

func (l *Ledger) Retry(channel string, now time.Time) (int, error) {
	count := 0
	for eventID, event := range l.Events {
		if event.ExpiresAt.IsZero() || !now.Before(event.ExpiresAt) {
			continue
		}
		for name, state := range event.Channels {
			if channel != "" && name != channel {
				continue
			}
			if state.Status != Blocked {
				continue
			}
			state.Status = Pending
			state.Kind = ""
			state.NextAttempt = time.Time{}
			event.Channels[name] = state
			count++
		}
		l.Events[eventID] = event
	}
	if count == 0 {
		return 0, errors.New("no blocked delivery for requested channel")
	}
	return count, nil
}

func (l Ledger) Validate() error {
	if l.Version != 2 {
		return errors.New("unsupported delivery ledger version")
	}
	for eventID, event := range l.Events {
		if eventID == "" || event.AsOf == "" || !supportedMessageVersion(event.Message.Version) || event.Message.EventID != eventID || event.ExpiresAt.IsZero() || len(event.Channels) == 0 {
			return errors.New("invalid persisted delivery ledger")
		}
		if _, err := time.Parse("2006-01-02", event.AsOf); err != nil {
			return errors.New("invalid persisted delivery date")
		}
		if event.Message.ExpiresAt.IsZero() || !event.Message.ExpiresAt.Equal(event.ExpiresAt) {
			return errors.New("delivery message expiry does not match event")
		}
		for channel, state := range event.Channels {
			if channel == "" || state.Attempts < 0 {
				return errors.New("invalid persisted delivery channel state")
			}
			switch state.Status {
			case Pending, InFlight, Delivered, Blocked, Expired, Unknowned:
			default:
				return errors.New("invalid persisted delivery status")
			}
		}
	}
	return nil
}

func supportedMessageVersion(version int) bool {
	return version == 1 || version == 2
}

func (l *Ledger) channelState(eventID, channel string) (ChannelState, error) {
	event, ok := l.Events[eventID]
	if !ok {
		return ChannelState{}, errors.New("delivery event not found")
	}
	state, ok := event.Channels[channel]
	if !ok {
		return ChannelState{}, errors.New("delivery channel not found")
	}
	return state, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
