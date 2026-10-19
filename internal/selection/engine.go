package selection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"time"
)

// Engine is a cooperative, in-process calculation boundary. Implementations
// must honor cancellation and return only stocks from the supplied frozen pool.
// Collection, coverage, persistence and notification are owned by the framework.
type Engine interface {
	Descriptor() EngineDescriptor
	Analyze(context.Context, Input) (Report, error)
}
type EngineDescriptor struct {
	ID            string   `json:"id"`
	Version       int      `json:"version"`
	Periods       []string `json:"periods"`
	Adjust        string   `json:"adjust"`
	RawRequired   bool     `json:"raw_required"`
	FinalOnly     bool     `json:"final_only"`
	MissingPolicy string   `json:"missing_policy"`
}
type Input struct {
	Version      int
	Config       config.Config // SafeConfig only; no paths, credentials or network clients.
	Histories    []market.History
	Raw          []market.History
	AsOf, Target string
	Now          time.Time
}
type Provenance struct {
	Engine     EngineDescriptor `json:"engine"`
	ConfigID   string           `json:"config_id"`
	Build      string           `json:"build"`
	Parameters json.RawMessage  `json:"parameters"`
}

func SafeConfig(c config.Config) config.Config {
	out := config.Config{Version: c.Version, Strategy: c.Strategy}
	out.Stocks = append([]config.Stock(nil), c.Stocks...)
	out.Analysis = c.Analysis
	out.Daily = c.Daily
	out.Selection = c.Selection
	out.Calendar = c.Calendar
	out.Calendar.Holidays = append([]string(nil), c.Calendar.Holidays...)
	out.Sync = c.Sync
	return out
}
func ConfigDigest(c config.Config) (string, json.RawMessage) {
	data, _ := json.Marshal(SafeConfig(c))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data
}

type builtinEngine struct{ descriptor EngineDescriptor }

func (e builtinEngine) Descriptor() EngineDescriptor { return e.descriptor }
func (e builtinEngine) Analyze(ctx context.Context, in Input) (Report, error) {
	return BuildWithExecution(ctx, in.Config, in.Histories, in.Raw, in.AsOf, in.Target, in.Now)
}
func DefaultEngine(c config.Config) (Engine, error) {
	id := "leader_daily_v1"
	if c.Strategy.Enabled {
		id = "short_horizon_v1"
	}
	if c.Analysis.Engine != "auto" && c.Analysis.Engine != "" && c.Analysis.Engine != id {
		return nil, errors.New("analysis.engine does not match enabled strategy")
	}
	return builtinEngine{EngineDescriptor{ID: id, Version: 1, Periods: []string{"day"}, Adjust: c.Daily.Adjust, RawRequired: c.Strategy.Enabled, FinalOnly: true, MissingPolicy: "exclude"}}, nil
}
func Execute(ctx context.Context, e Engine, in Input, build string) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	d := e.Descriptor()
	if d.ID == "" || d.Version != 1 || len(d.Periods) != 1 || d.Periods[0] != "day" || d.Adjust != in.Config.Daily.Adjust || !d.FinalOnly || d.MissingPolicy != "exclude" {
		return Report{}, errors.New("unsupported algorithm input contract")
	}
	if d.RawRequired && len(in.Raw) == 0 {
		return Report{}, errors.New("algorithm requires raw execution histories")
	}
	in.Version = 1
	in.Config = SafeConfig(in.Config)
	// Own a deep copy: an algorithm must not modify the durable collection input.
	data, err := json.Marshal(in)
	if err != nil {
		return Report{}, err
	}
	var frozen Input
	if err = json.Unmarshal(data, &frozen); err != nil {
		return Report{}, err
	}
	allowed := map[string]bool{}
	for _, s := range in.Config.Stocks {
		allowed[s.Code] = true
	}
	available, execution := map[string]bool{}, map[string]bool{}
	for _, h := range frozen.Histories {
		if !allowed[h.Code] || available[h.Code] || h.Adjust != d.Adjust {
			return Report{}, errors.New("algorithm history identity mismatch")
		}
		available[h.Code] = true
	}
	for _, h := range frozen.Raw {
		if !allowed[h.Code] || execution[h.Code] || h.Adjust != "none" {
			return Report{}, errors.New("algorithm raw history identity mismatch")
		}
		execution[h.Code] = true
	}
	if d.RawRequired {
		for code := range available {
			if !execution[code] {
				return Report{}, errors.New("algorithm raw history missing for stock")
			}
		}
	}
	for _, h := range append(append([]market.History{}, frozen.Histories...), frozen.Raw...) {
		if h.AsOf != in.AsOf || h.Validate() != nil {
			return Report{}, errors.New("invalid algorithm input history")
		}
	}
	result, err := e.Analyze(ctx, frozen)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if result.SelectionEnabled != in.Config.Selection.Enabled || (!in.Config.Selection.Enabled && len(result.Selected) > 0) || result.ID == "" || (result.Version != 1 && result.Version != 2) || result.AsOf != in.AsOf || result.Target != in.Target || result.Adjust != d.Adjust || result.Strategy != d.ID {
		return Report{}, errors.New("invalid algorithm result identity")
	}
	seen := map[string]bool{}
	for _, c := range result.Selected {
		if !available[c.Code] || seen[c.Code] {
			return Report{}, errors.New("invalid algorithm selected stock")
		}
		seen[c.Code] = true
	}
	ranked := map[string]bool{}
	for _, c := range result.Ranking {
		if !available[c.Code] || ranked[c.Code] {
			return Report{}, errors.New("invalid algorithm ranking stock")
		}
		ranked[c.Code] = true
	}
	if _, err := json.Marshal(result); err != nil {
		return Report{}, errors.New("invalid algorithm numeric result")
	}
	digest, parameters := ConfigDigest(in.Config)
	result.Provenance = &Provenance{Engine: d, ConfigID: digest, Build: build, Parameters: parameters}
	return result, nil
}
