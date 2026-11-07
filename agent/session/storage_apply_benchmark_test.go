package session

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

var applyBenchmarkResult *logState

// BenchmarkValidatedReplay compares the frozen original and final algorithms
// on the exact same fixed-timestamp decoded items and encoded lines. Setup is
// excluded. B/op reports cumulative allocations, not live or peak memory.
func BenchmarkValidatedReplay(b *testing.B) {
	for _, profile := range []string{"custom", "message", "mixed"} {
		for _, count := range []int{100, 1000} {
			b.Run(fmt.Sprintf("%s/%d", profile, count), func(b *testing.B) {
				fixture := storedLogCopyBenchmarkState(b, profile, count)
				var items []LogItem
				var lines [][]byte
				var totalBytes int
				for _, item := range fixture.log {
					line, err := json.Marshal(item)
					if err != nil {
						b.Fatal(err)
					}
					var decoded LogItem
					if err := decodeStrictJSON(line, &decoded); err != nil {
						b.Fatal(err)
					}
					items = append(items, decoded)
					lines = append(lines, line)
					totalBytes += len(line) + 1
				}
				// A single batch is legal up to 1024 entries; include its preflight and
				// unchanged return clone cost in the line replay comparison.
				batch, err := json.Marshal(wireBatch{Kind: "batch", Items: items})
				if err != nil {
					b.Fatal(err)
				}
				for _, mode := range []string{"decoded", "lines", "batch"} {
					run := func(original bool) (*logState, error) {
						state := newLogState(fixture.header)
						if mode == "batch" {
							if original {
								err = state.originalReadLine(batch)
							} else {
								err = state.readLine(batch)
							}
							return state, err
						}
						for i, item := range items {
							var err error
							if mode == "decoded" {
								if original {
									err = state.originalApply(item)
								} else {
									err = state.apply(item)
								}
							} else {
								if original {
									err = state.originalReadLine(lines[i])
								} else {
									err = state.readLine(lines[i])
								}
							}
							if err != nil {
								return nil, err
							}
						}
						return state, nil
					}
					old, err := run(true)
					if err != nil {
						b.Fatal(err)
					}
					next, err := run(false)
					if err != nil {
						b.Fatal(err)
					}
					if !reflect.DeepEqual(old, next) {
						b.Fatal("replay implementations disagree")
					}
					for _, original := range []bool{true, false} {
						name := "optimized"
						if original {
							name = "original"
						}
						b.Run(mode+"/"+name, func(b *testing.B) {
							b.ReportAllocs()
							b.ResetTimer()
							for i := 0; i < b.N; i++ {
								state, err := run(original)
								if err != nil {
									b.Fatal(err)
								}
								applyBenchmarkResult = state
							}
							b.StopTimer()
							// Logical per-item JSONL bytes, excluding the header and
							// batch envelope, reported consistently for all modes.
							b.ReportMetric(float64(totalBytes), "log-B")
						})
					}
				}
			})
		}
	}
}
