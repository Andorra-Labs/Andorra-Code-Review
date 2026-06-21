// Package configstore is the fork-owned ensemble-extension layer over the
// upstream config file (~/.opencodereview/config.json).
//
// It deliberately does NOT relocate upstream's Config struct from
// cmd/opencodereview/config_cmd.go. Upstream rebases must continue to apply
// cleanly. Instead, this package reads the same JSON file using
// map[string]json.RawMessage so every upstream block round-trips byte-for-byte,
// and owns ONLY the new "ensemble" top-level key.
package configstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ensembleKey is the only top-level JSON key this package reads or writes.
const ensembleKey = "ensemble"

// AndorraExt is the fork's extension to the on-disk config file.
type AndorraExt struct {
	Ensemble *EnsembleConfig `json:"ensemble,omitempty"`
}

// EnsembleConfig configures the multi-scanner + arbiter pipeline.
// When Enabled is false (or the whole block is absent), legacy single-model
// behavior runs.
type EnsembleConfig struct {
	Enabled  bool            `json:"enabled"`
	Scanners []ScannerSpec   `json:"scanners,omitempty"`
	Arbiter  *ArbiterSpec    `json:"arbiter,omitempty"`
	Dedup    *DedupConfig    `json:"dedup,omitempty"`
	Output   *EnsembleOutput `json:"output,omitempty"`
}

// ScannerSpec is one entry in the scanner ensemble. Provider must match either
// a preset provider name or a key under upstream's providers / custom_providers.
type ScannerSpec struct {
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Model       string   `json:"model,omitempty"`
	Weight      float64  `json:"weight,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	PromptTag   string   `json:"prompt_tag,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

// ArbiterSpec configures the arbiter pass that classifies grouped findings.
type ArbiterSpec struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Mode        string   `json:"mode,omitempty"`
}

// DedupConfig tunes the pre-arbiter grouping heuristic.
type DedupConfig struct {
	LineOverlapMinRatio    float64 `json:"line_overlap_min_ratio,omitempty"`
	TitleSimilarityMin     float64 `json:"title_similarity_min,omitempty"`
	RequireSamePath        bool    `json:"require_same_path,omitempty"`
	ExistingCodeExactBoost bool    `json:"existing_code_exact_boost,omitempty"`
}

// EnsembleOutput controls which verdict categories show in default output.
type EnsembleOutput struct {
	DefaultVerdicts []string `json:"default_verdicts,omitempty"`
	ShowProvenance  bool     `json:"show_provenance,omitempty"`
}

// DefaultPath returns the same path upstream uses for the config file. It is
// duplicated here so this package does not have to import package main.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".opencodereview", "config.json"), nil
}

// LoadAndorra reads the config file and returns the fork's ensemble extension.
// A missing file returns &AndorraExt{}, nil. Upstream blocks are not parsed and
// remain untouched on disk.
func LoadAndorra(path string) (*AndorraExt, error) {
	raw, err := loadRaw(path)
	if err != nil {
		return nil, err
	}
	ext := &AndorraExt{}
	if msg, ok := raw[ensembleKey]; ok {
		if err := json.Unmarshal(msg, &ext.Ensemble); err != nil {
			return nil, fmt.Errorf("parse %s block: %w", ensembleKey, err)
		}
	}
	return ext, nil
}

// SaveAndorra writes ext.Ensemble into the existing config file's "ensemble"
// key, preserving every other top-level key byte-for-byte. The write is atomic
// (temp file + os.Rename) and the file mode is 0o600 to match upstream.
//
// If ext.Ensemble is nil the key is removed from the file rather than written
// as a JSON null, so toggling ensemble off leaves the file as if it had never
// been touched by this package.
func SaveAndorra(path string, ext *AndorraExt) error {
	if ext == nil {
		ext = &AndorraExt{}
	}
	raw, err := loadRaw(path)
	if err != nil {
		return err
	}
	if ext.Ensemble == nil {
		delete(raw, ensembleKey)
	} else {
		buf, err := json.Marshal(ext.Ensemble)
		if err != nil {
			return fmt.Errorf("marshal ensemble: %w", err)
		}
		raw[ensembleKey] = buf
	}
	return writeRaw(path, raw)
}

// loadRaw reads the file and decodes it into a top-level key/RawMessage map.
// A missing file returns an empty map. Decoding into RawMessage means upstream
// values remain in their original JSON byte form and round-trip without
// reformatting.
func loadRaw(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(data) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	return raw, nil
}

// writeRaw serializes the top-level map and writes it atomically to path.
// The file is created with mode 0o600 to match upstream's saveConfig.
func writeRaw(path string, raw map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	out, err := json.MarshalIndent(raw, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
