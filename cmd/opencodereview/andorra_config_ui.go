package main

// andorra_config_ui.go wires the `ocr config ui` subcommand to the fork's
// internal/webui server. The dispatch case lives in andorra_config.go;
// this file provides the actual runConfigUI implementation that replaces
// the Phase 6 stub.

import (
	"fmt"
	"strings"

	"github.com/open-code-review/open-code-review/internal/webui"
)

// init swaps the andorra_config.go stub for the real implementation. By
// keeping the function name unchanged we avoid a second hook in upstream
// config_cmd.go.
func init() {
	configUIImpl = realConfigUI
}

// configUIImpl is the function actually invoked by runConfigUI. The
// init() above assigns the real implementation; before init it stays
// nil and runConfigUI returns the Phase 6 stub error.
var configUIImpl func(args []string) error

func realConfigUI(args []string) error {
	opts := webui.Options{
		Addr:        "localhost:5484",
		OpenBrowser: true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--addr" && i+1 < len(args):
			i++
			opts.Addr = args[i]
		case strings.HasPrefix(arg, "--addr="):
			opts.Addr = strings.TrimPrefix(arg, "--addr=")
		case arg == "--config" && i+1 < len(args):
			i++
			opts.ConfigPath = args[i]
		case strings.HasPrefix(arg, "--config="):
			opts.ConfigPath = strings.TrimPrefix(arg, "--config=")
		case arg == "--no-browser":
			opts.OpenBrowser = false
		case arg == "-h" || arg == "--help":
			printConfigUIUsage()
			return nil
		default:
			return fmt.Errorf("unknown flag for config ui: %s", arg)
		}
	}
	return webui.StartServer(opts)
}

func printConfigUIUsage() {
	fmt.Println(`Usage:
  ocr config ui [flags]

Launches the local web UI for editing ensemble settings (scanners, arbiter,
dedup thresholds, output verdict filter). Server binds to localhost by
default; CSRF + Host-header guard protect against drive-by browser tabs.

Flags:
  --addr <host:port>    bind address (default "localhost:5484")
  --config <path>       config file to edit (default ~/.opencodereview/config.json)
  --no-browser          do not auto-open the default browser

To export the result for CI, click "Export config for CI" in the UI or
run "ocr config export".`)
}
