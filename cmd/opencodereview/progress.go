package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// ProgressReporter writes machine-readable status snapshots that CI runners
// (e.g. GitHub Actions) can poll to keep a live status comment up to date.
// It is safe for concurrent use by the ensemble's scanner fan-out.
type ProgressReporter struct {
	path string
	mu   sync.Mutex
}

// NewProgressReporter returns a reporter that writes snapshots to path.
// If path is empty, Write becomes a no-op so callers can unconditionally
// report progress without checking for a configured file.
func NewProgressReporter(path string) *ProgressReporter {
	return &ProgressReporter{path: path}
}

// ProgressSnapshot is the public shape written to the progress file.
type ProgressSnapshot struct {
	TS        time.Time         `json:"ts"`
	Stage     string            `json:"stage"` // "scanners", "arbiter", "complete", "failed"
	Scanners  []ScannerProgress `json:"scanners"`
	Arbiter   ArbiterProgress   `json:"arbiter"`
	Message   string            `json:"message,omitempty"`
}

// ScannerProgress tracks one scanner's current state.
type ScannerProgress struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // "pending", "running", "complete", "error", "partial"
	Iteration int    `json:"iteration,omitempty"`
	Findings  int    `json:"findings,omitempty"`
}

// ArbiterProgress tracks the arbiter's current state.
type ArbiterProgress struct {
	Status string `json:"status"` // "pending", "running", "complete", "failed"
}

// Start initializes the snapshot with all scanners pending and the arbiter
// pending. Call once before any scanner work begins.
func (r *ProgressReporter) Start(scannerNames []string) error {
	scanners := make([]ScannerProgress, len(scannerNames))
	for i, n := range scannerNames {
		scanners[i] = ScannerProgress{Name: n, Status: "pending"}
	}
	return r.write(ProgressSnapshot{
		TS:       time.Now().UTC(),
		Stage:    "scanners",
		Scanners: scanners,
		Arbiter:  ArbiterProgress{Status: "pending"},
	})
}

// ScannerRunning marks a scanner as running and records the current
// 1-based iteration.
func (r *ProgressReporter) ScannerRunning(name string, iteration int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, err := r.readLocked()
	if err != nil {
		return err
	}
	snap.TS = time.Now().UTC()
	snap.Stage = "scanners"
	for i := range snap.Scanners {
		if snap.Scanners[i].Name == name {
			snap.Scanners[i].Status = "running"
			snap.Scanners[i].Iteration = iteration
			break
		}
	}
	return r.writeLocked(snap)
}

// ScannerComplete marks a scanner as finished. status should be one of
// "ok", "error", or "partial".
func (r *ProgressReporter) ScannerComplete(name string, iteration int, findings int, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, err := r.readLocked()
	if err != nil {
		return err
	}
	snap.TS = time.Now().UTC()
	for i := range snap.Scanners {
		if snap.Scanners[i].Name == name {
			snap.Scanners[i].Status = status
			snap.Scanners[i].Iteration = iteration
			snap.Scanners[i].Findings = findings
			break
		}
	}
	return r.writeLocked(snap)
}

// ArbiterRunning marks the arbiter stage as running.
func (r *ProgressReporter) ArbiterRunning() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, err := r.readLocked()
	if err != nil {
		return err
	}
	snap.TS = time.Now().UTC()
	snap.Stage = "arbiter"
	snap.Arbiter.Status = "running"
	return r.writeLocked(snap)
}

// ArbiterComplete marks the arbiter stage as finished.
func (r *ProgressReporter) ArbiterComplete(status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, err := r.readLocked()
	if err != nil {
		return err
	}
	snap.TS = time.Now().UTC()
	snap.Stage = "arbiter"
	snap.Arbiter.Status = status
	return r.writeLocked(snap)
}

// Complete marks the entire run as complete, preserving scanner/arbiter state.
func (r *ProgressReporter) Complete(message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, err := r.readLocked()
	if err != nil {
		return err
	}
	snap.TS = time.Now().UTC()
	snap.Stage = "complete"
	snap.Message = message
	return r.writeLocked(snap)
}

// Fail marks the entire run as failed with an explanatory message.
func (r *ProgressReporter) Fail(message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snap, err := r.readLocked()
	if err != nil {
		// If we can't read the previous state (e.g. the file never existed),
		// still write a minimal failed snapshot.
		snap = ProgressSnapshot{Stage: "failed", Arbiter: ArbiterProgress{Status: "pending"}}
	}
	snap.TS = time.Now().UTC()
	snap.Stage = "failed"
	snap.Message = message
	return r.writeLocked(snap)
}

func (r *ProgressReporter) readLocked() (ProgressSnapshot, error) {
	var snap ProgressSnapshot
	if r.path == "" {
		return snap, nil
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return snap, nil
		}
		return snap, err
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return snap, err
	}
	return snap, nil
}

func (r *ProgressReporter) write(snap ProgressSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeLocked(snap)
}

func (r *ProgressReporter) writeLocked(snap ProgressSnapshot) error {
	if r.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0644)
}
