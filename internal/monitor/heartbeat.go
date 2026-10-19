package monitor

import (
	"context"
	"time"
)

func (m *Monitor) heartbeat(ctx context.Context, id string) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := m.Market.TouchSyncTask(writeCtx, id, m.Now().UTC().Format(time.RFC3339Nano))
				stop()
				if err != nil && ctx.Err() == nil {
					m.Log.Warn("Task heartbeat failed", "task_id", id, "error", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
