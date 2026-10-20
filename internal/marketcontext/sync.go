package marketcontext

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/config"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/storage"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

var coreIndices = []struct {
	Code string
	Name string
}{
	{Code: "sh000001", Name: "上证指数"},
	{Code: "sz399001", Name: "深证成指"},
	{Code: "sz399006", Name: "创业板指"},
	{Code: "sh000300", Name: "沪深300"},
	{Code: "sh000688", Name: "科创50"},
}

type SyncError struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type SyncResult struct {
	AsOf      string      `json:"as_of"`
	Status    string      `json:"status"`
	Total     int         `json:"total"`
	Succeeded int         `json:"succeeded"`
	Skipped   int         `json:"skipped"`
	Failed    int         `json:"failed"`
	Indices   int         `json:"indices"`
	Boards    int         `json:"boards"`
	Errors    []SyncError `json:"errors,omitempty"`
}

type Syncer struct {
	config config.Config
	db     *storage.ContextDB
	client *Client
	log    *logging.Logger
	now    func() time.Time
}

func NewSyncer(c config.Config, db *storage.ContextDB, client *Client, log *logging.Logger) *Syncer {
	if log == nil {
		log = logging.Nop()
	}
	return &Syncer{config: c, db: db, client: client, log: log, now: time.Now}
}

func (s *Syncer) ForDate(ctx context.Context, asOf string) (SyncResult, error) {
	result := SyncResult{AsOf: asOf, Status: "failed", Indices: len(coreIndices)}
	if s == nil || s.db == nil {
		return result, errors.New("context storage is unavailable")
	}
	if parsed, err := time.Parse("2006-01-02", asOf); err != nil || parsed.Format("2006-01-02") != asOf {
		return result, errors.New("invalid context as-of date")
	}
	if s.client == nil {
		return result, errors.New("context client is unavailable")
	}
	started := s.now().In(shanghai)
	_ = s.db.SaveSyncStatus(ctx, storage.ContextSyncStatus{AsOf: asOf, StartedAt: started, FinishedAt: started, Status: "running"})

	boards, err := s.client.DiscoverBoards(ctx)
	if err != nil {
		_ = s.db.SaveSyncStatus(ctx, storage.ContextSyncStatus{AsOf: asOf, StartedAt: started, FinishedAt: s.now().In(shanghai), Status: "failed", ErrorKind: "discovery"})
		return result, err
	}
	result.Boards = len(boards)
	result.Total = len(coreIndices) + len(boards)

	lastDates, err := s.db.LastBarDates(ctx)
	if err != nil {
		return result, err
	}
	type instrument struct {
		storage.ContextInstrument
		from string
	}
	work := make([]instrument, 0, result.Total)
	for _, index := range coreIndices {
		from := lastDates[index.Code]
		if from >= asOf {
			result.Skipped++
			continue
		}
		work = append(work, instrument{ContextInstrument: storage.ContextInstrument{Code: index.Code, Name: index.Name, Kind: "index", Category: "index", Source: "tencent"}, from: from})
	}
	for _, board := range boards {
		from := lastDates[board.Code]
		if from >= asOf {
			result.Skipped++
			continue
		}
		work = append(work, instrument{ContextInstrument: storage.ContextInstrument{Code: board.Code, Name: board.Name, Kind: "board", Category: board.Category, Source: "eastmoney"}, from: from})
	}
	result.Succeeded = result.Skipped

	if len(work) > 0 {
		workers := s.config.MarketContext.Workers
		if workers < 1 {
			workers = 1
		}
		if workers > len(work) {
			workers = len(work)
		}
		jobs := make(chan instrument)
		out := make(chan workResult, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for item := range jobs {
					snapshot, err := s.fetchOne(ctx, item.ContextInstrument, item.from, asOf)
					if err != nil {
						if ctx.Err() != nil {
							out <- workResult{}
							continue
						}
						out <- workResult{error: &SyncError{Code: item.Code, Name: item.Name, Reason: contextError(err)}}
						continue
					}
					out <- workResult{snapshot: snapshot}
				}
			}()
		}
		go func() {
			defer close(jobs)
			for _, item := range work {
				select {
				case jobs <- item:
				case <-ctx.Done():
					return
				}
			}
		}()
		go func() {
			wg.Wait()
			close(out)
		}()
		pending := make([]storage.ContextSnapshot, 0, 20)
		flush := func() {
			if len(pending) == 0 {
				return
			}
			if err := s.db.SaveInstruments(ctx, pending); err != nil {
				result.Failed += len(pending)
				if len(result.Errors) < 40 {
					result.Errors = append(result.Errors, SyncError{Code: "batch", Name: "上下文批次", Reason: contextError(err)})
				}
			} else {
				result.Succeeded += len(pending)
			}
			pending = pending[:0]
		}
		for item := range out {
			if item.error != nil {
				result.Failed++
				if len(result.Errors) < 40 {
					result.Errors = append(result.Errors, *item.error)
				}
				continue
			}
			if item.snapshot == nil {
				continue
			}
			pending = append(pending, *item.snapshot)
			if len(pending) >= 20 {
				flush()
			}
		}
		flush()
	}
	if ctx.Err() != nil {
		result.Status = "partial"
		_ = s.db.SaveSyncStatus(context.Background(), storage.ContextSyncStatus{AsOf: asOf, StartedAt: started, FinishedAt: s.now().In(shanghai), Status: "partial", Total: result.Total, Succeeded: result.Succeeded, Failed: result.Failed, ErrorKind: "canceled"})
		return result, ctx.Err()
	}
	switch {
	case result.Succeeded == 0:
		result.Status = "failed"
	case result.Failed > 0:
		result.Status = "partial"
	default:
		result.Status = "complete"
	}
	errorKind := ""
	if result.Status != "complete" {
		errorKind = "instrument"
	}
	if err := s.db.SaveSyncStatus(ctx, storage.ContextSyncStatus{AsOf: asOf, StartedAt: started, FinishedAt: s.now().In(shanghai), Status: result.Status, Total: result.Total, Succeeded: result.Succeeded, Failed: result.Failed, ErrorKind: errorKind}); err != nil {
		return result, err
	}
	return result, nil
}

type workResult struct {
	error    *SyncError
	snapshot *storage.ContextSnapshot
}

func (s *Syncer) fetchOne(ctx context.Context, instrument storage.ContextInstrument, from, asOf string) (*storage.ContextSnapshot, error) {
	var bars []storage.ContextBar
	var name string
	var err error
	if instrument.Kind == "index" {
		bars, name, err = s.client.FetchIndex(ctx, instrument.Code, asOf)
	} else {
		bars, name, err = s.client.FetchBoard(ctx, Board{Code: instrument.Code, Name: instrument.Name, Category: instrument.Category}, asOf, from)
	}
	if err != nil {
		return nil, err
	}
	if len(bars) > 0 {
		if err := validateBars(instrument.Code, asOf, bars); err != nil {
			return nil, err
		}
	} else if from == "" {
		return nil, errors.New("no history returned")
	}
	if name != "" && instrument.Kind == "index" {
		instrument.Name = name
	}
	instrument.UpdatedAt = s.now().In(shanghai)
	return &storage.ContextSnapshot{Instrument: instrument, Bars: bars}, nil
}

func contextError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if strings.TrimSpace(message) == "" {
		return "unknown_error"
	}
	return fmt.Sprintf("%.240s", message)
}
