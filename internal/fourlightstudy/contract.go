// Package fourlightstudy evaluates the frozen four-light approximation across
// the current full-market universe. It does not rank stocks or place trades.
package fourlightstudy

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/calendar"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/market"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

const (
	Engine        = "four_light_market_wave_v1"
	EngineV2      = "four_light_market_wave_v2"
	PairingPolicy = strategy.WavePricePairPolicy
)

// Manifest is frozen alongside a SQLite backup, never a live database path.
// ObservationThrough is inclusive; ReservedFrom is exclusive. No data loader
// or evaluator may read prices on or after ReservedFrom.
type Manifest struct {
	Version               int                 `json:"schema_version"`
	WaveModel             string              `json:"wave_model,omitempty"`
	PairingPolicy         string              `json:"pairing_policy,omitempty"`
	DatabaseSHA256        string              `json:"database_sha256"`
	AsOf                  string              `json:"as_of"`
	From                  string              `json:"from"`
	Through               string              `json:"through"`
	ObservationThrough    string              `json:"observation_through"`
	ReservedFrom          string              `json:"reserved_from"`
	UniversePolicy        string              `json:"universe_policy"`
	ReferenceExperimentID string              `json:"reference_experiment_id,omitempty"`
	Calendar              experiment.Calendar `json:"calendar"`
	Parameters            config.Strategy     `json:"parameters"`
}

func (m Manifest) Config() config.Config {
	c := config.Defaults()
	c.Strategy = m.Parameters
	c.Daily.Adjust = "qfq"
	c.Calendar.ValidFrom, c.Calendar.ValidThrough = m.Calendar.From, m.Calendar.Through
	c.Calendar.TradingDays, c.Calendar.Holidays = m.Calendar.Days, m.Calendar.Holidays
	c.Calendar.Source, c.Calendar.Digest = m.Calendar.Source, m.Calendar.Digest
	return c
}

func (m Manifest) Validate() error {
	if (m.Version != 1 && m.Version != 2) || m.UniversePolicy != "current_active_sh_sz" {
		return errors.New("unsupported market-study manifest version or universe policy")
	}
	if (m.Version == 1 && (m.WaveModel != "" || m.PairingPolicy != "")) ||
		(m.Version == 2 && (m.WaveModel != strategy.UpwardWaveNoDrawdownModel || m.PairingPolicy != PairingPolicy)) {
		return errors.New("manifest wave model and pairing policy do not match schema version")
	}
	digest, err := hex.DecodeString(m.DatabaseSHA256)
	if err != nil || len(digest) != 32 || m.DatabaseSHA256 != strings.ToLower(m.DatabaseSHA256) {
		return errors.New("manifest requires a lowercase SHA256 of the frozen database")
	}
	for _, value := range []string{m.AsOf, m.From, m.Through, m.ObservationThrough, m.ReservedFrom, m.Calendar.From, m.Calendar.Through} {
		if parsed, err := time.Parse(time.DateOnly, value); err != nil || parsed.Format(time.DateOnly) != value {
			return errors.New("manifest dates must be YYYY-MM-DD")
		}
	}
	if m.From > m.Through || m.Through > m.ObservationThrough || m.ObservationThrough >= m.ReservedFrom || m.ReservedFrom > m.AsOf || m.Calendar.From > m.From || m.Calendar.Through < m.AsOf {
		return errors.New("signal, observation, reserved or calendar boundaries are inconsistent")
	}
	if m.Calendar.Version != 1 || len(m.Calendar.Days) == 0 || m.Calendar.Source == "" || m.Calendar.Digest == "" {
		return errors.New("market study requires a frozen trading calendar with provenance")
	}
	previous := ""
	for _, day := range m.Calendar.Days {
		if parsed, err := time.Parse(time.DateOnly, day); err != nil || parsed.Format(time.DateOnly) != day || day <= previous || day < m.Calendar.From || day > m.Calendar.Through {
			return errors.New("frozen calendar days must be valid, unique, sorted and in coverage")
		}
		previous = day
	}
	history, err := calendar.NewHistory(m.Calendar.Source, m.Calendar.From, m.Calendar.Through, m.Calendar.Days, time.Unix(1, 0))
	if err != nil || history.Digest != m.Calendar.Digest {
		return errors.New("frozen trading calendar digest mismatch")
	}
	if err := m.Parameters.Validate(); err != nil {
		return err
	}
	cal := calendar.New(m.Config())
	for _, day := range []string{m.From, m.Through, m.ObservationThrough, m.ReservedFrom} {
		if trading, err := cal.Trading(day); err != nil || !trading {
			return errors.New("signal and observation boundaries must be frozen trading sessions")
		}
	}
	return nil
}

func (m Manifest) waveModel() string {
	if m.Version == 2 {
		return m.WaveModel
	}
	return strategy.UpwardWaveModel
}

type Pair struct {
	Adjusted []market.Bar
	Raw      []market.Bar
	// Shares parallel the source-volume bars. V2 uses these only for pairing and
	// execution; four-light arithmetic retains its original provider units.
	AdjustedShares []float64
	RawShares      []float64
	AdjustedSource string
	RawSource      string
	// A stock-level provenance issue remains auditable, rather than silently
	// dropping the stock. Storage errors/cancellation use the error return.
	Problem string
}

type Source interface {
	Stocks(context.Context) ([]config.Stock, error)
	Load(context.Context, config.Stock) (Pair, error)
}

// Event contains only one code/date. Groups overlap intentionally (flip_all is
// a subset of first_all_red); reports must never add these rows as disjoint sets.
type Event struct {
	Code        string                     `json:"code"`
	Name        string                     `json:"name"`
	Date        string                     `json:"date"`
	Groups      []string                   `json:"groups"`
	Board       string                     `json:"board,omitempty"`
	VolumeRatio *float64                   `json:"volume_ratio,omitempty"`
	FourLight   strategy.FourLightEvidence `json:"four_light"`
	Wave        *strategy.UpwardWave       `json:"upward_wave"`
}
