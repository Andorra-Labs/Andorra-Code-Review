package configstore

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Set applies one dotted key=value mutation to the ensemble extension. Used by
// the CLI `ocr config set ensemble.*` path and by the web UI POST handlers.
// Keys not under `ensemble.` are rejected so this package only ever owns the
// ensemble block.
//
// Supported keys:
//
//	ensemble.scanners                      (JSON array of ScannerSpec)
//	ensemble.arbiter.provider              (string)
//	ensemble.arbiter.model                 (string)
//	ensemble.arbiter.mode                  (string)
//	ensemble.arbiter.temperature           (float)
//	ensemble.arbiter.max_tokens            (int)
//	ensemble.dedup.line_overlap_min_ratio  (float)
//	ensemble.dedup.title_similarity_min    (float)
//	ensemble.dedup.require_same_path       (bool)
//	ensemble.dedup.existing_code_exact_boost (bool)
//	ensemble.output.default_verdicts       (JSON array or comma-separated)
//	ensemble.output.show_provenance        (bool)
//
// `ensemble.enabled` is no longer supported: the ensemble path runs
// whenever scanners are configured. To disable ensemble mode, remove
// the scanner entries (`ocr config set ensemble.scanners '[]'`).
func Set(ext *AndorraExt, key, value string) error {
	if ext == nil {
		return fmt.Errorf("nil AndorraExt")
	}
	if !strings.HasPrefix(key, "ensemble.") && key != "ensemble" {
		return fmt.Errorf("configstore only owns ensemble.* keys; got %q", key)
	}
	ensureEnsemble(ext)

	switch key {
	case "ensemble.enabled":
		return fmt.Errorf("ensemble.enabled is no longer supported; ensemble mode now runs whenever scanners are configured. To disable, remove the scanners block (e.g. `ocr config set ensemble.scanners '[]'`)")
	case "ensemble.scanners":
		var scs []ScannerSpec
		if err := json.Unmarshal([]byte(value), &scs); err != nil {
			return fmt.Errorf("invalid JSON for ensemble.scanners: %w", err)
		}
		ext.Ensemble.Scanners = scs
	case "ensemble.arbiter.provider":
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.Provider = value
	case "ensemble.arbiter.model":
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.Model = value
	case "ensemble.arbiter.mode":
		ensureArbiter(ext)
		if !validArbiterMode(value) {
			return fmt.Errorf("ensemble.arbiter.mode %q is invalid (allowed: %v)", value, ValidArbiterModes[1:])
		}
		ext.Ensemble.Arbiter.Mode = value
	case "ensemble.arbiter.temperature":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float for ensemble.arbiter.temperature: %w", err)
		}
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.Temperature = &f
	case "ensemble.arbiter.max_tokens":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int for ensemble.arbiter.max_tokens: %w", err)
		}
		if n < 0 {
			return fmt.Errorf("ensemble.arbiter.max_tokens must be >= 0 (got %d)", n)
		}
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.MaxTokens = n
	case "ensemble.arbiter.bedrock":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool for ensemble.arbiter.bedrock: %w", err)
		}
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.Bedrock = b
	case "ensemble.arbiter.local":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool for ensemble.arbiter.local: %w", err)
		}
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.Local = b
	case "ensemble.arbiter.cost_per_m_input_usd":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 {
			return fmt.Errorf("invalid non-negative float for ensemble.arbiter.cost_per_m_input_usd: %v", value)
		}
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.CostPerMInputUSD = f
	case "ensemble.arbiter.cost_per_m_output_usd":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || f < 0 {
			return fmt.Errorf("invalid non-negative float for ensemble.arbiter.cost_per_m_output_usd: %v", value)
		}
		ensureArbiter(ext)
		ext.Ensemble.Arbiter.CostPerMOutputUSD = f
	case "ensemble.dedup.line_overlap_min_ratio":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float for ensemble.dedup.line_overlap_min_ratio: %w", err)
		}
		ensureDedup(ext)
		ext.Ensemble.Dedup.LineOverlapMinRatio = f
	case "ensemble.dedup.title_similarity_min":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float for ensemble.dedup.title_similarity_min: %w", err)
		}
		ensureDedup(ext)
		ext.Ensemble.Dedup.TitleSimilarityMin = f
	case "ensemble.dedup.require_same_path":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool for ensemble.dedup.require_same_path: %w", err)
		}
		ensureDedup(ext)
		ext.Ensemble.Dedup.RequireSamePath = b
	case "ensemble.dedup.existing_code_exact_boost":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool for ensemble.dedup.existing_code_exact_boost: %w", err)
		}
		ensureDedup(ext)
		ext.Ensemble.Dedup.ExistingCodeExactBoost = b
	case "ensemble.output.default_verdicts":
		ensureOutput(ext)
		ext.Ensemble.Output.DefaultVerdicts = parseStringList(value)
	case "ensemble.output.show_provenance":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool for ensemble.output.show_provenance: %w", err)
		}
		ensureOutput(ext)
		ext.Ensemble.Output.ShowProvenance = b
	default:
		return fmt.Errorf("unknown ensemble key: %s", key)
	}
	return nil
}

func ensureEnsemble(ext *AndorraExt) {
	if ext.Ensemble == nil {
		ext.Ensemble = &EnsembleConfig{}
	}
}

func ensureArbiter(ext *AndorraExt) {
	ensureEnsemble(ext)
	if ext.Ensemble.Arbiter == nil {
		ext.Ensemble.Arbiter = &ArbiterSpec{}
	}
}

func ensureDedup(ext *AndorraExt) {
	ensureEnsemble(ext)
	if ext.Ensemble.Dedup == nil {
		// Seed the default booleans so tuning a threshold via `ocr config set`
		// does not accidentally disable the same-path guard or exact-code boost.
		ext.Ensemble.Dedup = &DedupConfig{
			RequireSamePath:        true,
			ExistingCodeExactBoost: true,
		}
	}
}

func ensureOutput(ext *AndorraExt) {
	ensureEnsemble(ext)
	if ext.Ensemble.Output == nil {
		ext.Ensemble.Output = &EnsembleOutput{}
	}
}

func parseStringList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "[") {
		var out []string
		if err := json.Unmarshal([]byte(value), &out); err == nil {
			return normalizeList(out)
		}
		value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	}
	return normalizeList(strings.Split(value, ","))
}

func normalizeList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
