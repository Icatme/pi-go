// pi-superagent-eval prints a reproducible, wholly offline M9a experiment.
// All latencies are virtual; missing model/prompt baselines stay explicitly null.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Icatme/pi-go/internal/schedulereval"
)

func main() {
	summary := len(os.Args) == 2 && os.Args[1] == "-summary"
	if len(os.Args) != 1 && !summary {
		fmt.Fprintln(os.Stderr, "usage: pi-superagent-eval [-summary] > result.json (offline simulated time only)")
		os.Exit(2)
	}
	suite, err := schedulereval.RunSuite()
	var result any = suite
	if err == nil && summary {
		result, err = schedulereval.Summarize(suite)
	}
	if err == nil {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		err = enc.Encode(result)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
