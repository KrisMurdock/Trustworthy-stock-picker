package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"stock-watcher/internal/logging"
	"strconv"
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
	addressInfo := listener.Addr().(*net.TCPAddr)
	url := dashboardURL(addressInfo)
	logger.Info("Dashboard ready at "+url, "url", url, "listen", addressInfo.String())
	if addressInfo.IP.IsLoopback() {
		logger.Info("Dashboard accepts server-local connections; use SSH port forwarding for remote access")
	}
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

// Wildcard addresses describe a listener, not a browser destination. Advertise
// the matching loopback address; the listener itself is logged separately.
func dashboardURL(address *net.TCPAddr) string {
	ip := address.IP
	if ip.IsUnspecified() {
		if ip.To4() != nil {
			ip = net.IPv4(127, 0, 0, 1)
		} else {
			ip = net.IPv6loopback
		}
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(address.Port)) + "/"
}

type httpLogWriter struct{ logger *logging.Logger }

func (w httpLogWriter) Write(p []byte) (int, error) {
	// net/http diagnostics can include request data; keep it out of the log.
	w.logger.Warn("Dashboard HTTP connection error")
	return len(p), nil
}
