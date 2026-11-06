// pi-superagent-runtime-eval is an offline serial protocol probe. It never
// resolves model credentials or makes network calls.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Icatme/pi-go/internal/runtimeeval"
)

func main() {
	repeats := flag.Int("repeats", 5, "reproducibility repetitions per arm (1–5, not statistical samples)")
	flag.Parse()
	if err := run(*repeats); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(repeats int) error {
	dir, err := os.MkdirTemp("", "pi-runtime-eval-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suite, err := runtimeeval.Run(ctx, dir, repeats)
	if err != nil {
		return err
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(suite)
}
