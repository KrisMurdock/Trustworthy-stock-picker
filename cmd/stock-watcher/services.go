package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"stock-watcher/internal/logging"
	"time"
)

// runServices binds the dashboard before starting synchronization, and owns both
// lifetimes so a failed listener never leaves an invisible background monitor.
func runServices(ctx context.Context, address string, handler http.Handler, monitor func(context.Context) error, logger *logging.Logger) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("cannot start dashboard listener: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          log.New(httpLogWriter{logger}, "", 0),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	monitored := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("Dashboard listening", "url", "http://"+listener.Addr().String())
	go func() { monitored <- monitor(ctx) }()

	var result error
	var monitorFinished, serverFinished bool
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case result = <-monitored:
		monitorFinished = true
	case result = <-served:
		serverFinished = true
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		if result == nil || errors.Is(result, context.Canceled) {
			result = fmt.Errorf("dashboard shutdown failed: %w", err)
		}
	}
	if !serverFinished {
		<-served
	}
	if !monitorFinished {
		<-monitored
	}
	logger.Info("Dashboard stopped")
	return result
}

type httpLogWriter struct{ logger *logging.Logger }

func (w httpLogWriter) Write(p []byte) (int, error) {
	// net/http diagnostics can include request data; keep it out of the log.
	w.logger.Warn("Dashboard HTTP connection error")
	return len(p), nil
}
