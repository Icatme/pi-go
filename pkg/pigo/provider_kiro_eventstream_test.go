package pigo

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"reflect"
	"testing"
)

// Generated independently with Python struct.pack and zlib.crc32 from the
// Smithy wire format, not by the Go fixture encoder below.
const kiroGoldenFrame = "00000031000000160db7fb0e0d3a6d6573736167652d747970650700056576656e747b226f6b223a747275657ddfea2b3c"

func kiroTestGolden(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(kiroGoldenFrame)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func kiroTestFrame(headers, payload []byte) []byte {
	b := make([]byte, 12, 16+len(headers)+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(16+len(headers)+len(payload)))
	binary.BigEndian.PutUint32(b[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(b[8:12], crc32.ChecksumIEEE(b[:8]))
	b = append(b, headers...)
	b = append(b, payload...)
	var checksum [4]byte
	binary.BigEndian.PutUint32(checksum[:], crc32.ChecksumIEEE(b))
	return append(b, checksum[:]...)
}

type kiroFragmentReader struct {
	r *bytes.Reader
}

func (r kiroFragmentReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.r.Read(p)
}

func TestKiroEventStreamGoldenAndFragmentation(t *testing.T) {
	frame := kiroTestGolden(t)
	r := bytes.NewReader(append(append([]byte{}, frame...), kiroTestFrame(nil, nil)...))
	d, err := newKiroEventStreamDecoder(kiroFragmentReader{r}, len(frame))
	if err != nil {
		t.Fatal(err)
	}
	m, err := d.next()
	if err != nil || string(m.Payload) != `{"ok":true}` || !reflect.DeepEqual(m.Headers[":message-type"], kiroEventStreamHeader{Type: 7, Value: "event"}) {
		t.Fatalf("golden message = %#v, %v", m, err)
	}
	m, err = d.next()
	if err != nil || len(m.Headers) != 0 || len(m.Payload) != 0 {
		t.Fatalf("empty message = %#v, %v", m, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := d.next(); !errors.Is(err, io.EOF) {
			t.Fatalf("terminal EOF = %v", err)
		}
	}
}

func TestKiroEventStreamTruncated(t *testing.T) {
	frame := kiroTestGolden(t)
	for n := 0; n < len(frame); n++ {
		d, err := newKiroEventStreamDecoder(bytes.NewReader(frame[:n]), len(frame))
		if err != nil {
			t.Fatal(err)
		}
		want := io.ErrUnexpectedEOF
		if n == 0 {
			want = io.EOF
		}
		if _, err := d.next(); !errors.Is(err, want) {
			t.Errorf("prefix %d: got %v, want %v", n, err, want)
		}
	}
}

func TestKiroEventStreamChecksumFailureIsTerminal(t *testing.T) {
	for _, offset := range []int{0, 8, 12, 30, 48} {
		frame := kiroTestGolden(t)
		frame[offset] ^= 1
		stream := append(frame, kiroTestGolden(t)...)
		r := bytes.NewReader(stream)
		d, err := newKiroEventStreamDecoder(r, len(stream))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.next(); !errors.Is(err, errKiroEventStreamChecksum) {
			t.Fatalf("offset %d: %v", offset, err)
		}
		remaining := r.Len()
		if _, err := d.next(); !errors.Is(err, errKiroEventStreamChecksum) || r.Len() != remaining {
			t.Fatalf("failure must not consume or resynchronize: %v", err)
		}
	}
}

func TestKiroEventStreamLengthsAndBudget(t *testing.T) {
	for _, tc := range []struct {
		total, headers uint32
		budget         int
		want           error
	}{
		{15, 0, 1024, errKiroEventStreamLength},
		{16, 1, 1024, errKiroEventStreamLength},
		{0xffffffff, 0, 1024, errKiroEventStreamLimit},
		{17, 0, 16, errKiroEventStreamLimit},
	} {
		b := make([]byte, 12)
		binary.BigEndian.PutUint32(b[:4], tc.total)
		binary.BigEndian.PutUint32(b[4:8], tc.headers)
		binary.BigEndian.PutUint32(b[8:], crc32.ChecksumIEEE(b[:8]))
		// Only the prelude is available: rejection must precede a body read.
		d, err := newKiroEventStreamDecoder(bytes.NewReader(b), tc.budget)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.next(); !errors.Is(err, tc.want) {
			t.Errorf("total=%d headers=%d: %v", tc.total, tc.headers, err)
		}
	}
	if _, err := newKiroEventStreamDecoder(nil, 16); !errors.Is(err, errKiroEventStreamConfig) {
		t.Fatal("nil reader accepted")
	}
	for _, budget := range []int{-1, 0, 15} {
		if _, err := newKiroEventStreamDecoder(bytes.NewReader(nil), budget); !errors.Is(err, errKiroEventStreamConfig) {
			t.Fatalf("budget %d accepted", budget)
		}
	}
}

func TestKiroEventStreamHeaderTypes(t *testing.T) {
	for _, tc := range []struct {
		kind byte
		wire []byte
		want any
	}{
		{0, nil, true}, {1, nil, false},
		{2, []byte{255}, int8(-1)},
		{3, []byte{255, 254}, int16(-2)},
		{4, []byte{255, 255, 255, 253}, int32(-3)},
		{5, []byte{255, 255, 255, 255, 255, 255, 255, 252}, int64(-4)},
		{6, []byte{0, 2, 0, 255}, []byte{0, 255}},
		{7, []byte{0, 3, 0xe4, 0xb8, 0xad}, "中"},
		{8, []byte{0, 0, 0, 0, 0, 0, 0, 42}, int64(42)},
		{9, make([]byte, 16), [16]byte{}},
	} {
		headers, err := decodeKiroEventStreamHeaders(append([]byte{1, 'x', tc.kind}, tc.wire...))
		if err != nil || !reflect.DeepEqual(headers["x"], kiroEventStreamHeader{Type: tc.kind, Value: tc.want}) {
			t.Errorf("type %d: %#v, %v", tc.kind, headers, err)
		}
	}
}

func TestKiroEventStreamMalformedHeaders(t *testing.T) {
	cases := [][]byte{
		{0}, {1}, {1, 'x'}, {1, 255, 0}, {1, 'x', 10},
		{1, 'x', 0, 1, 'x', 1},
		{1, 'x', 6}, {1, 'x', 6, 0}, {1, 'x', 6, 0, 1},
		{1, 'x', 7, 0, 1, 255},
	}
	for kind, size := range map[byte]int{2: 1, 3: 2, 4: 4, 5: 8, 8: 8, 9: 16} {
		cases = append(cases, append([]byte{1, 'x', kind}, make([]byte, size-1)...))
	}
	for _, headers := range cases {
		frame := kiroTestFrame(headers, nil)
		d, err := newKiroEventStreamDecoder(bytes.NewReader(frame), len(frame))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.next(); !errors.Is(err, errKiroEventStreamHeader) {
			t.Errorf("headers %x: %v", headers, err)
		}
	}
}

func FuzzKiroEventStreamDecoder(f *testing.F) {
	golden, _ := hex.DecodeString(kiroGoldenFrame)
	f.Add(golden)
	f.Add(kiroTestFrame(nil, nil))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		d, err := newKiroEventStreamDecoder(bytes.NewReader(input), 64*1024)
		if err != nil {
			t.Fatal(err)
		}
		_, first := d.next()
		if first != nil {
			if _, second := d.next(); second != first {
				t.Fatalf("terminal error changed: %v -> %v", first, second)
			}
		}
	})
}

// Exercise header parsing directly; random frame mutation usually fails CRC
// verification before reaching these branches.
func FuzzKiroEventStreamHeaders(f *testing.F) {
	f.Add([]byte{1, 'x', 7, 0, 1, 'y'})
	f.Add([]byte{1, 'x', 0, 1, 'x', 1})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 64*1024 {
			t.Skip()
		}
		_, _ = decodeKiroEventStreamHeaders(input)
	})
}
