package main

// andorra_config.go owns fork-only `ocr config` subcommands: export, ui,
// scanners, arbiter. Upstream config_cmd.go remains untouched apart from a
// single switch case in runConfig that delegates here.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-code-review/open-code-review/internal/configstore"
)

// andorraConfig dispatches fork-only `ocr config` subcommands.
// Returns (handled, err): when handled is false, upstream runConfig keeps the
// request.
func andorraConfig(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "export":
		return true, runConfigExport(args[1:])
	case "ui":
		return true, runConfigUI(args[1:])
	case "set":
		// Intercept ensemble.* keys so the documented `ocr config set
		// ensemble.enabled true` non-interactive setup actually works.
		// Non-ensemble keys fall through to upstream's setConfigValue.
		if len(args) >= 3 && strings.HasPrefix(args[1], "ensemble") {
			return true, runConfigSetEnsemble(args[1], args[2])
		}
	}
	return false, nil
}

// runConfigSetEnsemble persists one ensemble.* key/value pair via configstore,
// preserving every other top-level config key byte-for-byte (see configstore
// docs for the raw-message merge contract).
func runConfigSetEnsemble(key, value string) error {
	path, err := defaultConfigPath()
	if err != nil {
		return err
	}
	ext, err := configstore.LoadAndorra(path)
	if err != nil {
		return fmt.Errorf("load ensemble config: %w", err)
	}
	if err := configstore.Set(ext, key, value); err != nil {
		return err
	}
	if err := configstore.SaveAndorra(path, ext); err != nil {
		return err
	}
	fmt.Printf("Set %s = %s\n", key, value)
	return nil
}

func runConfigExport(args []string) error {
	opts := exportOptions{
		mode:    configstore.ModePlaceholder,
		outPath: filepath.Join(".ocr", "config.json"),
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--placeholder-secrets":
			opts.mode = configstore.ModePlaceholder
		case arg == "--strip-secrets":
			opts.mode = configstore.ModeStrip
		case arg == "--out" && i+1 < len(args):
			i++
			opts.outPath = args[i]
		case strings.HasPrefix(arg, "--out="):
			opts.outPath = strings.TrimPrefix(arg, "--out=")
		case arg == "-h" || arg == "--help":
			printConfigExportUsage()
			return nil
		default:
			return fmt.Errorf("unknown flag for config export: %s", arg)
		}
	}

	srcPath, err := defaultConfigPath()
	if err != nil {
		return err
	}
	data, err := configstore.Export(srcPath, configstore.ExportOptions{Mode: opts.mode})
	if err != nil {
		return fmt.Errorf("export config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.outPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(opts.outPath, data, 0o644); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	switch opts.mode {
	case configstore.ModePlaceholder:
		fmt.Printf("Wrote %s with API keys replaced by ${env:NAME} placeholders.\n", opts.outPath)
		fmt.Printf("Commit this file and provide the referenced env vars in your CI secrets.\n")
	case configstore.ModeStrip:
		fmt.Printf("Wrote %s with API keys removed.\n", opts.outPath)
		fmt.Printf("Set them in CI via `ocr config set providers.<name>.api_key ...` or env vars.\n")
	}
	return nil
}

type exportOptions struct {
	mode    configstore.SecretMode
	outPath string
}

func printConfigExportUsage() {
	fmt.Println(`Usage:
  ocr config export [flags]

Writes a sanitized copy of ~/.opencodereview/config.json that is safe to commit.

Flags:
  --out <path>            output path (default ".ocr/config.json")
  --placeholder-secrets   (default) replace API keys with ${env:NAME} placeholders
  --strip-secrets         remove API key fields entirely

Examples:
  ocr config export
  ocr config export --out config/andorra.json
  ocr config export --strip-secrets --out .github/ocr-config.json

The exported file preserves every upstream block (provider, model, llm,
telemetry, ensemble, ...) and only sanitizes secret-shaped fields under
providers / custom_providers / llm.`)
}

// runConfigUI dispatches to the local web config UI. The implementation is
// installed by an init() in andorra_config_ui.go which assigns configUIImpl;
// before that init runs (or in a build without that file) the function falls
// back to a clear error.
func runConfigUI(args []string) error {
	if configUIImpl == nil {
		return fmt.Errorf("config ui: not available in this build")
	}
	return configUIImpl(args)
}
