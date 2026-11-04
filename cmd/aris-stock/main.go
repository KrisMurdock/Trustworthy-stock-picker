// aris-stock is an offline research binary. It does not import production
// configuration, databases, market clients, recommendation or notification code.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/aris"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/logging"
	"github.com/KrisMurdock/Trustworthy-stock-picker/internal/maexec"
)

var version = "development"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(execute(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func buildID() (string, error) {
	path, e := os.Executable()
	if e != nil {
		return "", e
	}
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func readJSON(path string, v any) error {
	if path == "" {
		return errors.New("input path is required")
	}
	raw, e := aris.Read(path, aris.MaxDocument)
	if e != nil {
		return e
	}
	return aris.Decode(raw, v)
}
func execute(ctx context.Context, args []string, out, stderr io.Writer) int {
	fail := func(e error) int { fmt.Fprintln(stderr, e); return 1 }
	if len(args) == 0 {
		fmt.Fprintln(stderr, "aris-stock: prepare | simulate | seal | hash | run | status | resume | cancel | verify | serve | version")
		return 1
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	request := fs.String("request", "", "frozen bundle JSON")
	input := fs.String("input", "", "simulator input JSON")
	runID := fs.String("run", "", "run ID")
	stateDir := fs.String("state-dir", ".scratch/aris-stock", "local research state")
	logDir := fs.String("log-dir", "", "override log directory")
	machine := fs.Bool("json", false, "machine JSON on stdout; logs on stderr")
	addr := fs.String("addr", "127.0.0.1:9090", "loopback HTTP address")
	catalogPath := fs.String("catalog", "", "operator catalog JSON: name to bundle file")
	snapshotPath := fs.String("snapshot", "", "schema 1 snapshot JSON or gzip")
	signalsPath := fs.String("signals", "", "preregistered signals JSON")
	sessionsPath := fs.String("sessions", "", "ordered session dates JSON")
	configPath := fs.String("model-config", "", "explicit simulator configuration JSON")
	limitMode := fs.String("limit-mode", "board", "board or five_percent")
	timeout := fs.Duration("timeout", 10*time.Minute, "diagnostic preparation/simulation deadline")
	if e := fs.Parse(args[1:]); e != nil {
		return 1
	}
	if fs.NArg() != 0 {
		return fail(errors.New("unexpected positional argument"))
	}
	emit := func(v any) int {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		if e := enc.Encode(v); e != nil {
			return fail(e)
		}
		return 0
	}
	build, e := buildID()
	if e != nil {
		return fail(e)
	}
	switch cmd {
	case "version":
		info, _ := debug.ReadBuildInfo()
		return emit(map[string]any{"version": version, "build_sha256": build, "build_info": info})
	case "hash":
		var in maexec.Input
		if e = readJSON(*input, &in); e != nil {
			return fail(e)
		}
		return emit(map[string]string{"input_sha256": aris.Digest(in)})
	case "seal":
		var b aris.Bundle
		if e = readJSON(*request, &b); e != nil {
			return fail(e)
		}
		b.Contract.Build = build
		b.Contract.HumanHash = aris.Hash([]byte(b.HumanContract))
		b.Contract.InputHashes = map[string]string{}
		for id, in := range b.Inputs {
			b.Contract.InputHashes[id] = aris.Digest(in)
		}
		if e = b.Validate(build); e != nil {
			return fail(e)
		}
		return emit(b)
	case "prepare", "simulate":
		if *timeout <= 0 || *timeout > 24*time.Hour {
			return fail(errors.New("diagnostic timeout must be in (0,24h]"))
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		if cmd == "simulate" {
			var in maexec.Input
			if e = readJSON(*input, &in); e != nil {
				return fail(e)
			}
			r, e := maexec.Simulate(ctx, in)
			if e != nil {
				return fail(e)
			}
			return emit(r)
		}
		var snap maexec.Snapshot
		var sessions []string
		var signals []maexec.Signal
		var cfg maexec.Config
		if e = readJSON(*sessionsPath, &sessions); e != nil {
			return fail(e)
		}
		if e = readJSON(*signalsPath, &signals); e != nil {
			return fail(e)
		}
		if e = readJSON(*configPath, &cfg); e != nil {
			return fail(e)
		}
		raw, e := aris.Read(*snapshotPath, aris.MaxDocument)
		if e != nil {
			return fail(e)
		}
		if strings.HasSuffix(*snapshotPath, ".gz") {
			z, e := gzip.NewReader(bytes.NewReader(raw))
			if e != nil {
				return fail(e)
			}
			raw, e = io.ReadAll(io.LimitReader(z, aris.MaxDocument+1))
			z.Close()
			if e != nil {
				return fail(e)
			}
		}
		if e = aris.Decode(raw, &snap); e != nil {
			return fail(e)
		}
		in, _, e := maexec.Prepare(ctx, snap, sessions, signals, cfg, *limitMode)
		if e != nil {
			return fail(e)
		}
		return emit(in)
	case "run", "status", "resume", "cancel", "verify", "serve":
	default:
		return fail(errors.New("unknown command"))
	}
	opts, e := logging.EnvOptions()
	if e != nil {
		return fail(e)
	}
	opts.Name = "aris-stock"
	if *logDir != "" {
		opts.Dir = *logDir
	}
	opts.Stdout = out
	if *machine {
		opts.Stdout = stderr
	}
	if e = aris.SafePath(opts.Dir); e != nil {
		return fail(e)
	}
	logger, e := logging.New(opts)
	if e != nil {
		return fail(e)
	}
	defer logger.Close()
	app, e := aris.New(*stateDir, build, logger)
	if e != nil {
		return fail(e)
	}
	defer app.Close()
	if endpoint := os.Getenv("ARIS_REVIEW_ENDPOINT"); endpoint != "" {
		token := os.Getenv("ARIS_REVIEW_TOKEN")
		logger.RegisterSecret(token)
		reviewer, e := aris.NewHTTPReviewer(endpoint, token, os.Getenv("ARIS_REVIEW_MODEL"), os.Getenv("ARIS_REVIEW_FAMILY"))
		if e != nil {
			return fail(e)
		}
		app.Reviewer = reviewer
	}
	var state aris.State
	switch cmd {
	case "run":
		var b aris.Bundle
		if e = readJSON(*request, &b); e == nil {
			state, e = app.Run(ctx, b)
		}
	case "resume":
		state, e = app.Resume(ctx, *runID)
	case "status":
		state, e = app.Status(*runID)
		if e != nil {
			return fail(e)
		}
		return emit(state)
	case "cancel":
		if e = app.Cancel(*runID); e != nil {
			return fail(e)
		}
		return emit(map[string]string{"run_id": *runID, "status": "cancellation_requested"})
	case "verify":
		verifyCtx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		if e = app.Replay(verifyCtx, *runID); e != nil {
			return fail(e)
		}
		return emit(map[string]any{"run_id": *runID, "verified": true, "scope": "frozen bundle, deterministic results, gates and published report"})
	case "serve":
		var paths map[string]string
		if e = readJSON(*catalogPath, &paths); e != nil {
			return fail(e)
		}
		if len(paths) > 100 {
			return fail(errors.New("catalog limit is 100 bundles"))
		}
		catalog := map[string]aris.Bundle{}
		total := 0
		for name, path := range paths {
			if !filepath.IsAbs(path) {
				path = filepath.Join(filepath.Dir(*catalogPath), path)
			}
			raw, e := aris.Read(path, aris.MaxDocument)
			if e != nil {
				return fail(e)
			}
			total += len(raw)
			if total > aris.MaxDocument {
				return fail(errors.New("catalog aggregate size exceeds limit"))
			}
			var b aris.Bundle
			if e = aris.Decode(raw, &b); e != nil {
				return fail(e)
			}
			catalog[name] = b
		}
		handler, e := aris.NewHandler(ctx, app, catalog)
		if e != nil {
			return fail(e)
		}
		defer handler.Close()
		logger.Info("Research HTTP service starting", "address", *addr)
		if e = aris.Serve(ctx, *addr, handler); e != nil {
			return fail(e)
		}
		return 0
	}
	if e != nil {
		return fail(e)
	}
	if n := emit(state); n != 0 {
		return n
	}
	switch state.Status {
	case "accepted":
		return 0
	case "rejected":
		return 2
	case "needs_human":
		return 3
	case "canceled":
		return 4
	case "development_passed":
		return 5
	default:
		return 1
	}
}
