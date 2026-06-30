// Command smoketest is a throwaway target for the Andorra Review workflow.
//
// It exists so a pull request changes a *reviewable* code file. A docs-only
// diff is filtered out by the reviewer's extension allowlist and never reaches
// the LLM (0 tokens, 0s, a misleading "0 findings"); a Go file is allowlisted,
// so the scanner actually calls the model over the tailnet. The function below
// is deliberately rough to give the reviewer something concrete to flag.
//
// Delete this directory once the smoke test has confirmed the pipeline works.
package main

import (
	"fmt"
	"os"
)

// readFirstArg reads the file named by the first CLI argument and returns its
// contents. It is intentionally sloppy (no bounds check, ignored error) so the
// ensemble has real findings to surface during the smoke test.
func readFirstArg() string {
	path := os.Args[1]            // no length check: panics when run with no args
	data, _ := os.ReadFile(path) // error intentionally dropped
	return string(data)
}

func main() {
	fmt.Println(readFirstArg())
}
