package experiment_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/experiment"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/strategy"
)

func TestUpwardWaveArchiveRoundTripAndReservedInterval(t *testing.T) {
	ctx := context.Background()
	snapshot, record := holdoutLifecycleFixture(t)
	db, err := storage.OpenExperiments(ctx, t.TempDir(), false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Start(ctx, snapshot, record); err != nil {
		t.Fatal(err)
	}
	if err = experiment.Evaluate(ctx, snapshot, &record, nil); err != nil {
		t.Fatal(err)
	}
	if record.UpwardWave == nil || record.UpwardWave.Model != strategy.UpwardWaveModel || record.UpwardWave.Windows[0].Selected == 0 {
		t.Fatal("wave evaluation missing", record.UpwardWave)
	}
	for _, tr := range record.Result.Development.Trades {
		if !tr.Selected {
			continue
		}
		if tr.UpwardWave == nil {
			t.Fatal("selected trade missing label")
		}
		for _, d := range tr.UpwardWave.Days {
			if d.Date >= record.Result.Development.HoldoutFrom {
				t.Fatal("reserved path leaked", d)
			}
		}
	}
	if err = db.Finish(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := db.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatal(err)
	}
	a, _ := json.Marshal(record.UpwardWave)
	b, _ := json.Marshal(got.Result.Development.UpwardWave)
	list, _ := json.Marshal(items[0].UpwardWave)
	if string(a) != string(b) || string(a) != string(list) {
		t.Fatal("list/detail wave summary differs")
	}
	if items[0].Result != nil {
		t.Fatal("list leaked trade paths")
	}
	if consumed, err := db.HoldoutConsumed(ctx, snapshot.ID); err != nil || consumed {
		t.Fatal("development consumed holdout", err)
	}
	if err = db.Finish(ctx, record); err == nil {
		t.Fatal("terminal record changed")
	}
	old := []byte(`{"schema_version":1,"result":{"development":{}}}`)
	var legacy experiment.Record
	if err = json.Unmarshal(old, &legacy); err != nil || legacy.UpwardWave != nil || legacy.Result.Development.UpwardWave != nil {
		t.Fatal("legacy evidence invented", err)
	}
}
