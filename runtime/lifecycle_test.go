package runtime

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
)

func TestServicesRejectOccupiedPortBeforeMonitorStarts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := false
	err = runServices(context.Background(), listener.Addr().String(), http.NotFoundHandler(), func(context.Context) error {
		started = true
		return nil
	}, logging.Nop())
	if err == nil || started {
		t.Fatalf("occupied port must fail before monitor starts: %v, started=%v", err, started)
	}
}

func TestServicesWaitForMonitorCleanupOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runServices(ctx, "127.0.0.1:0", http.NotFoundHandler(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}, logging.Nop())
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected shutdown error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service shutdown blocked")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("returned without waiting for monitor cleanup")
	}
}

func TestServicesPropagateMonitorFailure(t *testing.T) {
	want := errors.New("monitor failed")
	err := runServices(context.Background(), "127.0.0.1:0", http.NotFoundHandler(), func(context.Context) error {
		return want
	}, logging.Nop())
	if !errors.Is(err, want) {
		t.Fatalf("monitor error lost: %v", err)
	}
}

func TestIndependentMonitorsStartAndJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	stopped := make(chan struct{}, 2)
	done := make(chan error, 1)
	runner := func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return ctx.Err()
	}
	go func() { done <- runMonitors(ctx, runner, runner) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("one monitor blocked another")
		}
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) || len(stopped) != 2 {
			t.Fatal("did not join all monitors", e)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
	<-stopped
	<-stopped
	want := errors.New("storage unavailable")
	if e := runMonitors(context.Background(), runner, func(context.Context) error { return want }); !errors.Is(e, want) {
		t.Fatal("fatal cause lost", e)
	}
}
