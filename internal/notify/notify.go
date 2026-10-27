package notify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/articlechart"
)

type Message struct {
	Version        int       `json:"version"`
	EventID        string    `json:"event_id"`
	DisplayEventID string    `json:"display_event_id,omitempty"`
	PartLabel      string    `json:"part_label,omitempty"`
	Title          string    `json:"title"`
	Subtitle       string    `json:"subtitle,omitempty"`
	HeaderIcon     string    `json:"header_icon,omitempty"`
	WidthMode      string    `json:"width_mode,omitempty"`
	PlainText      string    `json:"plain_text,omitempty"`
	Article        *Article  `json:"article,omitempty"`
	Sections       []Section `json:"sections"`
	Template       string    `json:"template,omitempty"`
	TrimPolicy     string    `json:"trim_policy,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// Article is the structured form used by article-only channels. PlainText
// remains the human-readable rendering for chat channels.
type Article struct {
	Title  string              `json:"title"`
	Digest string              `json:"digest,omitempty"`
	Body   string              `json:"body"`
	Charts []articlechart.Spec `json:"charts,omitempty"`
}

type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type TableColumnKind string

const (
	TableText   TableColumnKind = "text"
	TableStatus TableColumnKind = "status"
	TableChange TableColumnKind = "change"
)

type TableTone string

const (
	ToneDefault  TableTone = ""
	TonePositive TableTone = "positive"
	ToneNegative TableTone = "negative"
)

type TableColumn struct {
	Key   string          `json:"key"`
	Label string          `json:"label"`
	Kind  TableColumnKind `json:"kind"`
	Width string          `json:"width,omitempty"`
	Align string          `json:"align,omitempty"`
}

type TableCell struct {
	Text string    `json:"text"`
	Tone TableTone `json:"tone,omitempty"`
}

type Table struct {
	PageSize          int           `json:"page_size,omitempty"`
	RowHeight         string        `json:"row_height,omitempty"`
	FreezeFirstColumn bool          `json:"freeze_first_column,omitempty"`
	Columns           []TableColumn `json:"columns"`
	Rows              [][]TableCell `json:"rows"`
}

type Section struct {
	Heading  string   `json:"heading,omitempty"`
	Lines    []string `json:"lines"`
	Fields   []Field  `json:"fields,omitempty"`
	Table    *Table   `json:"table,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

const TrimNone = "none"

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
	Part      string                  `json:"part,omitempty"`
	Sequence  int                     `json:"sequence,omitempty"`
	DependsOn string                  `json:"depends_on,omitempty"`
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
	return l.EnqueuePart(asof, message, channels, "", 0, "")
}

func (l *Ledger) EnqueuePart(asof string, message Message, channels []string, part string, sequence int, dependsOn string) error {
	if l.Version == 0 {
		l.Version = 2
	}
	if l.Version != 2 {
		return errors.New("unsupported delivery ledger version")
	}
	if !supportedMessageVersion(message.Version) || message.EventID == "" || asof == "" || message.ExpiresAt.IsZero() || sequence < 0 || dependsOn == message.EventID {
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
			Part:      part,
			Sequence:  sequence,
			DependsOn: dependsOn,
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
		if terminal && !l.dependencyPending(eventID) {
			delete(l.Events, eventID)
			changed = true
		}
	}
	return changed
}

func (l Ledger) dependencyPending(eventID string) bool {
	for _, event := range l.Events {
		if event.DependsOn != eventID {
			continue
		}
		for _, state := range event.Channels {
			switch state.Status {
			case Delivered, Blocked, Expired:
			default:
				return true
			}
		}
	}
	return false
}

func (l Ledger) Due(now time.Time) []Delivery {
	eventIDs := make([]string, 0, len(l.Events))
	for eventID := range l.Events {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Slice(eventIDs, func(i, j int) bool {
		left, right := l.Events[eventIDs[i]], l.Events[eventIDs[j]]
		if left.AsOf != right.AsOf {
			return left.AsOf < right.AsOf
		}
		if left.Sequence != right.Sequence {
			return left.Sequence < right.Sequence
		}
		return eventIDs[i] < eventIDs[j]
	})
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

func (l Ledger) CanDeliver(eventID, channel string) bool {
	event, ok := l.Events[eventID]
	if !ok || event.DependsOn == "" {
		return ok
	}
	dependency, ok := l.Events[event.DependsOn]
	if !ok {
		return false
	}
	state, ok := dependency.Channels[channel]
	return ok && state.Status == Delivered
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
		if eventID == "" || event.AsOf == "" || !supportedMessageVersion(event.Message.Version) || event.Message.EventID != eventID || event.ExpiresAt.IsZero() || len(event.Channels) == 0 || event.Sequence < 0 || event.DependsOn == eventID {
			return errors.New("invalid persisted delivery ledger")
		}
		if _, err := time.Parse("2006-01-02", event.AsOf); err != nil {
			return errors.New("invalid persisted delivery date")
		}
		if event.Message.ExpiresAt.IsZero() || !event.Message.ExpiresAt.Equal(event.ExpiresAt) {
			return errors.New("delivery message expiry does not match event")
		}
		if event.DependsOn != "" {
			if dependency, ok := l.Events[event.DependsOn]; ok {
				if dependency.Sequence >= event.Sequence {
					return errors.New("invalid delivery dependency order")
				}
			} else if !terminalEvent(event) {
				return errors.New("missing delivery dependency")
			}
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

func terminalEvent(event Event) bool {
	for _, state := range event.Channels {
		switch state.Status {
		case Delivered, Blocked, Expired:
		default:
			return false
		}
	}
	return true
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
