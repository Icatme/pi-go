// pi-superagent-runtime-load exercises concurrent offline runtime conservation.
// It has no provider, credential, network, or live-model option.
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
	repeats := flag.Int("repeats", 5, "conservation repetitions per arm (1–5, not statistical samples)")
	flag.Parse()
	if err := run(*repeats); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(repeats int) error {
	dir, err := os.MkdirTemp("", "pi-runtime-load-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suite, err := runtimeeval.RunLoad(ctx, dir, repeats)
	if err != nil {
		return err
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(suite)
}
