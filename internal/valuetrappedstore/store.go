// Package valuetrappedstore loads the durable value-and-trapped observation
// without making the pure intersection package depend on storage.
package valuetrappedstore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/valuetrapped"
)

// LoadLatestRecommendation reads the latest artifact from all-market.db. A
// missing database or artifact returns fs.ErrNotExist so the delivery loop can
// distinguish "not generated yet" from a storage outage.
func LoadLatestRecommendation(ctx context.Context, root string, requireCIFS bool) (valuetrapped.Recommendation, error) {
	if _, err := os.Stat(filepath.Join(root, "all-market.db")); errors.Is(err, fs.ErrNotExist) {
		return valuetrapped.Recommendation{}, fs.ErrNotExist
	} else if err != nil {
		return valuetrapped.Recommendation{}, err
	}
	db, err := storage.OpenAllMarketView(root, requireCIFS)
	if err != nil {
		return valuetrapped.Recommendation{}, err
	}
	defer db.Close()
	artifact, err := db.LatestRecommendation(ctx, valuetrapped.Market)
	if errors.Is(err, fs.ErrNotExist) || (err != nil && strings.Contains(err.Error(), "no such table")) {
		return valuetrapped.Recommendation{}, fs.ErrNotExist
	}
	if err != nil {
		return valuetrapped.Recommendation{}, err
	}
	var rec valuetrapped.Recommendation
	if err := json.Unmarshal(artifact.Payload, &rec); err != nil {
		return rec, errors.New("invalid stored value-trapped recommendation")
	}
	if rec.Version != valuetrapped.SchemaVersion || rec.ID != artifact.ID || rec.AsOf != artifact.AsOf || rec.Target != artifact.Target || rec.Market != valuetrapped.Market {
		return rec, errors.New("stored value-trapped recommendation identity mismatch")
	}
	return rec, nil
}
