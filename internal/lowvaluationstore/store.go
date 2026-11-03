// Package lowvaluationstore loads the durable full-market low-valuation
// recommendation without making the pure valuation package depend on storage.
package lowvaluationstore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/lowvaluation"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

// LoadLatestRecommendation reads the latest artifact from all-market.db. A
// missing database or artifact returns fs.ErrNotExist so the delivery loop can
// distinguish "not generated yet" from a storage outage.
func LoadLatestRecommendation(ctx context.Context, root string, requireCIFS bool) (lowvaluation.Recommendation, error) {
	if _, err := os.Stat(filepath.Join(root, "all-market.db")); errors.Is(err, fs.ErrNotExist) {
		return lowvaluation.Recommendation{}, fs.ErrNotExist
	} else if err != nil {
		return lowvaluation.Recommendation{}, err
	}
	db, err := storage.OpenAllMarketView(root, requireCIFS)
	if err != nil {
		return lowvaluation.Recommendation{}, err
	}
	defer db.Close()
	artifact, err := db.LatestRecommendation(ctx, lowvaluation.Market)
	if errors.Is(err, fs.ErrNotExist) || (err != nil && strings.Contains(err.Error(), "no such table")) {
		return lowvaluation.Recommendation{}, fs.ErrNotExist
	}
	if err != nil {
		return lowvaluation.Recommendation{}, err
	}
	var rec lowvaluation.Recommendation
	if err := json.Unmarshal(artifact.Payload, &rec); err != nil {
		return rec, errors.New("invalid stored low valuation recommendation")
	}
	if rec.Version != lowvaluation.SchemaVersion || rec.ID != artifact.ID || rec.AsOf != artifact.AsOf || rec.Target != artifact.Target || rec.Market != lowvaluation.Market {
		return rec, errors.New("stored low valuation recommendation identity mismatch")
	}
	return rec, nil
}

// Quotes adapts the all-market storage row to the pure valuation quote type.
type Quotes struct {
	DB *storage.AllMarketDB
}

func (q Quotes) LatestQuotes(ctx context.Context, asOf string) ([]lowvaluation.Quote, error) {
	if q.DB == nil {
		return nil, fs.ErrNotExist
	}
	rows, err := q.DB.LatestQuotes(ctx, asOf)
	if err != nil {
		return nil, err
	}
	out := make([]lowvaluation.Quote, 0, len(rows))
	for _, row := range rows {
		out = append(out, lowvaluation.Quote{Code: row.Code, Name: row.Name, PriceDate: row.PriceDate,
			Close: row.Close, Volume: row.Volume})
	}
	return out, nil
}
