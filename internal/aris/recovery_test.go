package aris

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCrashChild(t *testing.T) {
	root := os.Getenv("ARIS_TEST_CRASH_ROOT")
	if root == "" {
		return
	}
	a, e := New(root, "test-build", nil)
	if e != nil {
		t.Fatal(e)
	}
	b := configured()
	b.Contract.Budget.Attempts = 2
	s, dir, unlock, e := a.freeze(context.Background(), b)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	s.Attempts = append(s.Attempts, Attempt{ID: "base-1", Candidate: "base", Status: "started"})
	if e = a.save(dir, s); e != nil {
		t.Fatal(e)
	}
	fmt.Println("ready")
	<-time.After(30 * time.Second)
}
func TestKilledProcessResumesWithoutRefund(t *testing.T) {
	root := t.TempDir()
	// Race-instrumented children also fsync several files before handshaking.
	// Allow bounded startup under the full storage suite's I/O load.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashChild$")
	child.Env = append(os.Environ(), "ARIS_TEST_CRASH_ROOT="+root)
	stdout, e := child.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	child.Stderr = os.Stderr
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatal("child never reached durable started record")
	}
	a, _ := New(root, "test-build", nil)
	if _, e = a.Resume(context.Background(), "test"); e == nil {
		t.Fatal("second writer acquired live lock")
	}
	if e = child.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	child.Wait()
	s, e := a.Resume(context.Background(), "test")
	if e != nil || s.Status != "rejected" {
		t.Fatalf("%+v %v", s, e)
	}
	if len(s.Attempts) != 3 || s.Attempts[0].Status != "interrupted" || s.Attempts[1].ID != "base-2" {
		t.Fatal(s.Attempts)
	}
	if e = a.Verify("test"); e != nil {
		t.Fatal(e)
	}
}
func TestCancellationAndExhaustedRecovery(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRun), func(t *testing.T) {
			a, _ := New(t.TempDir(), "test-build", nil)
			b := configured()
			s, dir, unlock, e := a.freeze(context.Background(), b)
			if e != nil {
				t.Fatal(e)
			}
			s.Attempts = append(s.Attempts, Attempt{ID: "base-1", Candidate: "base", Status: "started"})
			if e = a.save(dir, s); e != nil {
				t.Fatal(e)
			}
			unlock()
			if cancelRun {
				if e = a.Cancel("test"); e != nil {
					t.Fatal(e)
				}
			}
			s, e = a.Resume(context.Background(), "test")
			if e != nil {
				t.Fatal(e)
			}
			want := "needs_human"
			if cancelRun {
				want = "canceled"
			}
			if s.Status != want || len(s.Attempts) != 1 {
				t.Fatalf("%+v", s)
			}
		})
	}
}
func TestMalformedStateAndSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	a, _ := New(root, "test-build", nil)
	if _, e := a.Run(context.Background(), bundle()); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "runs/test/state.json")
	if e := os.WriteFile(path, []byte(`{"schema_version":1,"schema_version":1}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Resume(context.Background(), "test"); e == nil {
		t.Fatal("repaired invalid state")
	}
	link := filepath.Join(root, "link")
	if e := os.Symlink(root, link); e != nil {
		t.Fatal(e)
	}
	if _, e := New(link, "test-build", nil); e == nil {
		t.Fatal("accepted link")
	}
}

func TestReadRejectsFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if e := syscall.Mkfifo(path, 0600); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := Read(path, 100); done <- e }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("accepted FIFO")
		}
	case <-time.After(time.Second):
		f, e := os.OpenFile(path, os.O_RDWR, 0600)
		if e == nil {
			f.Close()
		}
		<-done
		t.Fatal("blocked opening FIFO")
	}
}
