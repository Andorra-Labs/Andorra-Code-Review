package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProgressReporterLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.json")

	rep := NewProgressReporter(path)
	if err := rep.Start([]string{"spark", "gpt"}); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	snap := readSnapshot(t, path)
	if snap.Stage != "scanners" {
		t.Errorf("stage=%q, want scanners", snap.Stage)
	}
	if len(snap.Scanners) != 2 {
		t.Fatalf("scanners=%d, want 2", len(snap.Scanners))
	}
	if snap.Scanners[0].Status != "pending" {
		t.Errorf("scanner0 status=%q, want pending", snap.Scanners[0].Status)
	}
	if snap.Arbiter.Status != "pending" {
		t.Errorf("arbiter status=%q, want pending", snap.Arbiter.Status)
	}

	if err := rep.ScannerRunning("spark", 1); err != nil {
		t.Fatalf("ScannerRunning failed: %v", err)
	}
	snap = readSnapshot(t, path)
	if snap.Scanners[0].Status != "running" || snap.Scanners[0].Iteration != 1 {
		t.Errorf("scanner0 after running=%+v", snap.Scanners[0])
	}

	if err := rep.ScannerComplete("spark", 1, 3, "ok"); err != nil {
		t.Fatalf("ScannerComplete failed: %v", err)
	}
	snap = readSnapshot(t, path)
	if snap.Scanners[0].Status != "ok" || snap.Scanners[0].Findings != 3 {
		t.Errorf("scanner0 after complete=%+v", snap.Scanners[0])
	}

	if err := rep.ArbiterRunning(); err != nil {
		t.Fatalf("ArbiterRunning failed: %v", err)
	}
	snap = readSnapshot(t, path)
	if snap.Stage != "arbiter" || snap.Arbiter.Status != "running" {
		t.Errorf("arbiter after running: stage=%q arbiter=%+v", snap.Stage, snap.Arbiter)
	}

	if err := rep.ArbiterComplete("ok"); err != nil {
		t.Fatalf("ArbiterComplete failed: %v", err)
	}
	if err := rep.Complete("done"); err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	snap = readSnapshot(t, path)
	if snap.Stage != "complete" || snap.Message != "done" {
		t.Errorf("final snapshot: stage=%q message=%q", snap.Stage, snap.Message)
	}
}

func TestProgressReporterNoPath(t *testing.T) {
	rep := NewProgressReporter("")
	if err := rep.Start([]string{"a"}); err != nil {
		t.Fatalf("Start with empty path should no-op: %v", err)
	}
	if err := rep.Complete("ok"); err != nil {
		t.Fatalf("Complete with empty path should no-op: %v", err)
	}
}

func TestProgressReporterFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress.json")

	rep := NewProgressReporter(path)
	_ = rep.Start([]string{"a"})
	if err := rep.Fail("boom"); err != nil {
		t.Fatalf("Fail failed: %v", err)
	}
	snap := readSnapshot(t, path)
	if snap.Stage != "failed" || snap.Message != "boom" {
		t.Errorf("failed snapshot: stage=%q message=%q", snap.Stage, snap.Message)
	}
}

func readSnapshot(t *testing.T, path string) ProgressSnapshot {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read progress file: %v", err)
	}
	var snap ProgressSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal progress: %v", err)
	}
	return snap
}
