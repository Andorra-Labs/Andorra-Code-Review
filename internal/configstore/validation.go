package configstore

import "fmt"

// ValidArbiterModes is the set of accepted ArbiterSpec.Mode values. An empty
// string is also accepted and treated as the default ("per_file").
var ValidArbiterModes = []string{"", "per_file", "per_group"}

// Validate returns a list of human-readable errors describing every invariant
// the ensemble extension violates. An empty return value means the extension is
// safe to use. Validation is structural only; cross-checks against upstream
// providers / custom_providers happen in Phase 2 via a separate helper.
func Validate(ext *AndorraExt) []error {
	if ext == nil || ext.Ensemble == nil {
		return nil
	}
	var errs []error
	e := ext.Ensemble
	if e.Enabled {
		active := countEnabledScanners(e.Scanners)
		if active < 2 {
			errs = append(errs, fmt.Errorf("ensemble.enabled requires at least 2 enabled scanners (got %d active out of %d configured)", active, len(e.Scanners)))
		}
		if e.Arbiter == nil {
			errs = append(errs, fmt.Errorf("ensemble.enabled requires ensemble.arbiter to be configured"))
		}
	}
	seenNames := map[string]struct{}{}
	for i, s := range e.Scanners {
		// Disabled scanners are not run, so skip structural checks on them.
		if s.Enabled != nil && !*s.Enabled {
			continue
		}
		if s.Name == "" {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d]: name is required", i))
		} else if _, dup := seenNames[s.Name]; dup {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d]: duplicate name %q", i, s.Name))
		} else {
			seenNames[s.Name] = struct{}{}
		}
		if s.Provider == "" && !s.Bedrock {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d] (%q): provider is required (or set bedrock=true)", i, s.Name))
		}
		if s.Bedrock && s.Model == "" {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d] (%q): bedrock scanners require a non-empty model id", i, s.Name))
		}
		if s.Weight < 0 {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d] (%q): weight must be >= 0 (got %g)", i, s.Name, s.Weight))
		}
		if s.MaxTokens < 0 {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d] (%q): max_tokens must be >= 0 (got %d)", i, s.Name, s.MaxTokens))
		}
		if s.Bedrock && s.Local {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d] (%q): bedrock and local are mutually exclusive", i, s.Name))
		}
		if s.CostPerMInputUSD < 0 || s.CostPerMOutputUSD < 0 {
			errs = append(errs, fmt.Errorf("ensemble.scanners[%d] (%q): cost rates must be >= 0", i, s.Name))
		}
	}
	if e.Arbiter != nil {
		if e.Arbiter.Provider == "" && !e.Arbiter.Bedrock {
			errs = append(errs, fmt.Errorf("ensemble.arbiter.provider is required (or set ensemble.arbiter.bedrock=true)"))
		}
		if e.Arbiter.Bedrock && e.Arbiter.Model == "" {
			errs = append(errs, fmt.Errorf("ensemble.arbiter: bedrock arbiter requires a non-empty model id"))
		}
		if !validArbiterMode(e.Arbiter.Mode) {
			errs = append(errs, fmt.Errorf("ensemble.arbiter.mode %q is invalid (allowed: %v)", e.Arbiter.Mode, ValidArbiterModes[1:]))
		}
		if e.Arbiter.MaxTokens < 0 {
			errs = append(errs, fmt.Errorf("ensemble.arbiter.max_tokens must be >= 0 (got %d)", e.Arbiter.MaxTokens))
		}
		if e.Arbiter.Bedrock && e.Arbiter.Local {
			errs = append(errs, fmt.Errorf("ensemble.arbiter: bedrock and local are mutually exclusive"))
		}
		if e.Arbiter.CostPerMInputUSD < 0 || e.Arbiter.CostPerMOutputUSD < 0 {
			errs = append(errs, fmt.Errorf("ensemble.arbiter: cost rates must be >= 0"))
		}
	}
	if e.Dedup != nil {
		if e.Dedup.LineOverlapMinRatio < 0 || e.Dedup.LineOverlapMinRatio > 1 {
			errs = append(errs, fmt.Errorf("ensemble.dedup.line_overlap_min_ratio must be in [0,1] (got %g)", e.Dedup.LineOverlapMinRatio))
		}
		if e.Dedup.TitleSimilarityMin < 0 || e.Dedup.TitleSimilarityMin > 1 {
			errs = append(errs, fmt.Errorf("ensemble.dedup.title_similarity_min must be in [0,1] (got %g)", e.Dedup.TitleSimilarityMin))
		}
	}
	return errs
}

func validArbiterMode(mode string) bool {
	for _, m := range ValidArbiterModes {
		if mode == m {
			return true
		}
	}
	return false
}

// countEnabledScanners returns the number of ScannerSpec entries whose
// Enabled flag is unset (treated as active) or true. Used by Validate so a
// config with two scanners — one Enabled: false — surfaces the problem at
// config time rather than at review time.
func countEnabledScanners(specs []ScannerSpec) int {
	n := 0
	for _, s := range specs {
		if s.Enabled == nil || *s.Enabled {
			n++
		}
	}
	return n
}
