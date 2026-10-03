package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func transactionEntry(id string) NewEntry {
	return NewEntry{ID: id, Type: EntryTypeCustom, Custom: &CustomData{CustomType: "test", Payload: []byte(`{"value":1}`)}}
}

func TestBranchTransactionConflictIsolationAndAtomicity(t *testing.T) {
	for _, factory := range storageCases() {
		t.Run(factory.name, func(t *testing.T) {
			s := factory.new(t)
			base, err := s.ReadBranch(MainLane)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CreateLane("other", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AppendEntry("other", transactionEntry("other")); err != nil {
				t.Fatal(err)
			}
			v, entries, err := s.CompareAppend(base.Version, []NewEntry{transactionEntry("a"), transactionEntry("b")})
			if err != nil || len(entries) != 2 || entries[1].ParentID != "a" || v.Head != "b" {
				t.Fatalf("batch=%+v version=%+v err=%v", entries, v, err)
			}
			if _, _, err := s.CompareAppend(base.Version, []NewEntry{transactionEntry("stale")}); errorCode(err) != ErrorConflict {
				t.Fatalf("stale: %v", err)
			}
			if err := s.MoveLane(MainLane, "a"); err != nil {
				t.Fatal(err)
			}
			if err := s.MoveLane(MainLane, "b"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.CompareAppend(v, []NewEntry{transactionEntry("aba")}); errorCode(err) != ErrorConflict {
				t.Fatalf("ABA: %v", err)
			}
			base, _ = s.ReadBranch(MainLane)
			if _, _, err := s.CompareAppend(base.Version, []NewEntry{transactionEntry("uncommitted"), transactionEntry("b")}); err == nil {
				t.Fatal("invalid suffix accepted")
			}
			if _, ok := s.Entry("uncommitted"); ok {
				t.Fatal("batch prefix published")
			}
			if got, _ := s.ReadBranch(MainLane); got.Version != base.Version {
				t.Fatal("invalid batch changed revision")
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for i := range 2 {
				wg.Go(func() {
					_, _, err := s.CompareAppend(base.Version, []NewEntry{transactionEntry(fmt.Sprintf("race-%d", i))})
					results <- err
				})
			}
			wg.Wait()
			close(results)
			wins, conflicts := 0, 0
			for err := range results {
				if err == nil {
					wins++
				} else if errorCode(err) == ErrorConflict {
					conflicts++
				} else {
					t.Fatal(err)
				}
			}
			if wins != 1 || conflicts != 1 {
				t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
			}
		})
	}
}

func TestBranchTransactionUsesNormalizedEntryIDs(t *testing.T) {
	for _, factory := range storageCases() {
		for _, duplicate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/duplicate=%v", factory.name, duplicate), func(t *testing.T) {
				s := factory.new(t)
				base, err := s.ReadBranch(MainLane)
				if err != nil {
					t.Fatal(err)
				}
				secondID := "second"
				if duplicate {
					secondID = string([]byte{0xfe})
				}
				_, entries, err := s.CompareAppend(base.Version, []NewEntry{transactionEntry(string([]byte{0xff})), transactionEntry(secondID)})
				if duplicate {
					if err == nil {
						t.Fatal("normalized duplicate IDs accepted")
					}
					after, readErr := s.ReadBranch(MainLane)
					if readErr != nil || after.Version != base.Version || len(after.Entries) != 0 {
						t.Fatalf("rejected batch changed storage: %+v %v", after, readErr)
					}
				} else if err != nil || len(entries) != 2 || entries[1].ParentID != entries[0].ID {
					t.Fatalf("normalized parent chain: %+v %v", entries, err)
				}
				if disk, ok := s.(*JSONLStorage); ok {
					if err := disk.Close(); err != nil {
						t.Fatal(err)
					}
					reopened, err := OpenJSONLStorage(disk.Path())
					if err != nil {
						t.Fatal(err)
					}
					defer reopened.Close()
				}
			})
		}
	}
}

func TestJSONLTransactionRecoveryAndSyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transaction.jsonl")
	s, err := CreateJSONLStorage(path, Header{ID: "transaction"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	base, _ := s.ReadBranch(MainLane)
	_, _, err = s.CompareAppend(base.Version, []NewEntry{transactionEntry("a"), transactionEntry("b")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// A torn transaction has no visible prefix on recovery.
	appendBytes(t, path, []byte(`{"kind":"batch","items":[{"kind":"entry","seq":3`))
	s, err = OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	base, err = s.ReadBranch(MainLane)
	if err != nil || len(base.Entries) != 2 {
		t.Fatalf("recovery=%+v err=%v", base, err)
	}
	cause := errors.New("disk sync failed")
	s.syncFile = func() error { return cause }
	_, _, err = s.CompareAppend(base.Version, []NewEntry{transactionEntry("c"), transactionEntry("d")})
	if !errors.Is(err, cause) {
		t.Fatalf("sync cause lost: %v", err)
	}
	if len(s.Entries()) != 2 {
		t.Fatal("failed transaction published")
	}
	_ = s.Close()
	reopened, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(reopened.Entries()) != 2 {
		t.Fatal("failed transaction became durable")
	}
}

func TestSessionWriterProcessHelper(t *testing.T) {
	path := os.Getenv("PIGO_SESSION_LOCK_TEST_PATH")
	if path == "" {
		return
	}
	s, err := OpenJSONLStorage(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println("locked")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
	_ = s.Close()
	os.Exit(0)
}

func TestSessionWriterExcludesHandlesAliasesAndProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exclusive.jsonl")
	s, err := CreateJSONLStorage(path, Header{ID: "exclusive"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if other, err := OpenJSONLStorage(path); errorCode(err) != ErrorBusy {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("second writer: %v", err)
	}
	alias := filepath.Join(filepath.Dir(path), "alias.jsonl")
	if err := os.Link(path, alias); err == nil {
		if other, err := OpenJSONLStorage(alias); errorCode(err) != ErrorBusy {
			if other != nil {
				_ = other.Close()
			}
			t.Fatalf("alias writer: %v", err)
		}
	}
	if _, err := ReadJSONLHeader(path); err != nil {
		t.Fatalf("read-only inspection blocked: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionWriterProcessHelper$")
	cmd.Env = append(os.Environ(), "PIGO_SESSION_LOCK_TEST_PATH="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatalf("child did not lock: %q err=%v", scanner.Text(), scanner.Err())
	}
	if other, err := OpenJSONLStorage(path); errorCode(err) != ErrorBusy {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("process writer: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	reopened, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatalf("lock retained after process death: %v", err)
	}
	_ = reopened.Close()
}

func TestTransactionCrashProcessHelper(t *testing.T) {
	path := os.Getenv("PIGO_TRANSACTION_CRASH")
	if path == "" {
		return
	}
	s, err := CreateJSONLStorage(path, Header{ID: "crash-stages"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendEntry(MainLane, transactionEntry("base")); err != nil {
		t.Fatal(err)
	}
	branch, _ := s.ReadBranch(MainLane)
	entries := []NewEntry{transactionEntry("first"), transactionEntry("second")}
	items, err := s.state.prepareBatch(branch.Version, entries)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(wireBatch{Kind: "batch", Items: items})
	switch os.Getenv("PIGO_TRANSACTION_STAGE") {
	case "before-write":
	case "partial":
		if _, err := s.file.Write(raw[:len(raw)/2]); err != nil {
			t.Fatal(err)
		}
		if err := s.file.Sync(); err != nil {
			t.Fatal(err)
		}
	case "no-newline":
		if _, err := s.file.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := s.file.Sync(); err != nil {
			t.Fatal(err)
		}
	case "flushed":
		if _, err := s.writer.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
		if err := s.writer.Flush(); err != nil {
			t.Fatal(err)
		}
	case "synced":
		s.syncFile = func() error {
			if err := s.file.Sync(); err != nil {
				return err
			}
			os.Exit(72)
			return nil
		}
		if _, _, err := s.CompareAppend(branch.Version, entries); err != nil {
			t.Fatal(err)
		}
	case "applied":
		if _, _, err := s.CompareAppend(branch.Version, entries); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unknown stage")
	}
	os.Exit(72)
}

func TestTransactionProcessCrashStages(t *testing.T) {
	for _, stage := range []string{"before-write", "partial", "no-newline", "flushed", "synced", "applied"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transaction.jsonl")
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTransactionCrashProcessHelper$")
			cmd.Env = append(os.Environ(), "PIGO_TRANSACTION_CRASH="+path, "PIGO_TRANSACTION_STAGE="+stage)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 72 {
				t.Fatalf("helper: %v %s", err, output)
			}
			s, err := OpenJSONLStorage(path)
			if stage == "no-newline" {
				if errorCode(err) != ErrorCorruptLog {
					if s != nil {
						_ = s.Close()
					}
					t.Fatalf("unterminated complete record accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			want := 3
			if stage == "before-write" || stage == "partial" {
				want = 1
			}
			if got := len(s.Entries()); got != want {
				t.Fatalf("partial transaction: entries=%d want=%d", got, want)
			}
		})
	}
}
