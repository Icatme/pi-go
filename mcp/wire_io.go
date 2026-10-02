package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
)

// Reader observes newline-delimited input without changing its original bytes.
// A complete bounded frame is validated before any part reaches SDK decoding.
// The underlying closer must unblock pending reads, as required by SDK IOTransport.
func (o *Observer) Reader(reader io.ReadCloser) io.ReadCloser {
	return &observedReader{observer: o, source: reader, reader: bufio.NewReader(reader)}
}

// Writer buffers partial writes until a complete newline-delimited frame can be
// checked. It refuses repeated physical requests before writing their bytes.
func (o *Observer) Writer(writer io.WriteCloser) io.WriteCloser {
	return &observedWriter{observer: o, target: writer}
}

type observedReader struct {
	observer *Observer
	source   io.ReadCloser
	reader   *bufio.Reader
	mu       sync.Mutex
	buffer   []byte
	terminal error
}

func (r *observedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buffer) == 0 {
		if r.terminal != nil {
			return 0, r.terminal
		}
		frame, err := readWireLine(r.reader, r.observer.limits.MaxFrameBytes)
		if len(frame) > 0 && !isBlankWireLine(frame) {
			if observeErr := r.observer.observeFrame(frame, false); observeErr != nil {
				r.terminal = observeErr
				r.observer.failPending(observeErr)
				return 0, observeErr
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			r.terminal = err
			r.observer.failPending(err)
			return 0, err
		}
		r.buffer = frame
		r.terminal = err
	}
	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]
	if n == 0 && r.terminal != nil {
		return 0, r.terminal
	}
	return n, nil
}

func (r *observedReader) Close() error { return r.source.Close() }

func readWireLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var frame []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > limit {
			return nil, ErrWireLimit
		}
		frame = append(frame, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return frame, err
		}
	}
}

func isBlankWireLine(frame []byte) bool { return len(bytes.TrimSpace(frame)) == 0 }

type observedWriter struct {
	observer *Observer
	target   io.WriteCloser
	mu       sync.Mutex
	buffer   []byte
	terminal error
}

func (w *observedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.terminal != nil {
		return 0, w.terminal
	}
	consumed := 0
	physicalBytes := 0
	for len(p) > 0 {
		n := bytes.IndexByte(p, '\n')
		if n < 0 {
			n = len(p)
		} else {
			n++
		}
		if len(w.buffer)+n > w.observer.limits.MaxFrameBytes {
			w.terminal = ErrWireLimit
			return consumed, w.terminal
		}
		w.buffer = append(w.buffer, p[:n]...)
		p = p[n:]
		consumed += n
		if w.buffer[len(w.buffer)-1] != '\n' {
			continue
		}
		if !isBlankWireLine(w.buffer) {
			if err := w.observer.observeFrame(w.buffer, true); err != nil {
				// A locally denied/cancelled complete frame wrote no bytes. Its
				// logical child context is cancelled so the SDK can return this
				// real error without poisoning the shared session write path.
				w.buffer = w.buffer[:0]
				localRejection := errors.Is(err, ErrDispatchDenied) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
				if !localRejection || physicalBytes != 0 {
					w.terminal = err
				}
				return consumed - n, err
			}
		}
		written, err := w.target.Write(w.buffer)
		physicalBytes += written
		if err == nil && written != len(w.buffer) {
			err = io.ErrShortWrite
		}
		w.buffer = w.buffer[:0]
		if err != nil {
			w.terminal = err
			return consumed, err
		}
	}
	return consumed, nil
}

func (w *observedWriter) Close() error { return w.target.Close() }
