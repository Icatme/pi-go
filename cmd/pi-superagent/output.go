package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Icatme/pi-go/superagent"
)

type frameEncoder interface{ Encode(any) error }

// One staged observation batch. Cursors acknowledge staging or a completed
// direct write, never external transport delivery or durable consumption.
type outputBatch struct {
	reports  []superagent.Report
	replies  []superagent.Reply
	failures []superagent.MainFailure
}

// Only the dispatcher appends. If the stdout goroutine falls behind, spool
// replies in order without blocking controls or growing an in-memory queue.
// Ownership of each sealed file passes to the writer through ready.
type frameQueue struct {
	ready chan any
	spill *os.File
	err   error
}

func (q *frameQueue) Encode(frame any) error {
	if q.err != nil {
		return q.err
	}
	if q.spill == nil {
		select {
		case q.ready <- frame:
			return nil
		default:
		}
		q.spill, q.err = os.CreateTemp("", "pi-superagent-output-*.jsonl")
	}
	if q.err == nil {
		q.err = writeFrame(json.NewEncoder(q.spill), frame)
	}
	if q.err != nil {
		q.err = fmt.Errorf("buffering CLI output: %w", q.err)
	}
	return q.err
}

func (q *frameQueue) spillReady() chan<- any {
	if q.spill == nil {
		return nil
	}
	return q.ready
}

func (q *frameQueue) finish() {
	if q.spill != nil {
		q.ready <- q.spill
	}
	close(q.ready)
}

func writeFrame(out frameEncoder, frame any) error {
	if batch, ok := frame.(outputBatch); ok {
		if err := writeReports(out, batch.reports); err != nil {
			return err
		}
		for _, reply := range batch.replies {
			if err := out.Encode(struct {
				Type string `json:"type"`
				superagent.Reply
			}{"REPLY", reply}); err != nil {
				return err
			}
		}
		for _, failure := range batch.failures {
			if err := out.Encode(struct {
				Type string `json:"type"`
				superagent.MainFailure
			}{"MAIN_ERROR", failure}); err != nil {
				return err
			}
		}
		return nil
	}
	if reports, ok := frame.([]superagent.Report); ok {
		return writeReports(out, reports)
	}
	return out.Encode(frame)
}

func replayFrames(out frameEncoder, file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dec := json.NewDecoder(file)
	var frame json.RawMessage
	for {
		if err := dec.Decode(&frame); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if err := out.Encode(frame); err != nil {
			return err
		}
	}
}
