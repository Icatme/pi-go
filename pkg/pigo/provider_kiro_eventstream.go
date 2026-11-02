package pigo

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"unicode/utf8"
)

var (
	errKiroEventStreamConfig   = errors.New("kiro event stream: invalid decoder configuration")
	errKiroEventStreamLength   = errors.New("kiro event stream: invalid message length")
	errKiroEventStreamLimit    = errors.New("kiro event stream: message exceeds configured size limit")
	errKiroEventStreamChecksum = errors.New("kiro event stream: invalid checksum")
	errKiroEventStreamHeader   = errors.New("kiro event stream: invalid or duplicate header")
)

// kiroEventStreamHeader preserves the wire type. Timestamp values remain int64
// milliseconds and UUID values are [16]byte; neither is interpreted as identity.
type kiroEventStreamHeader struct {
	Type  byte
	Value any
}

type kiroEventStreamMessage struct {
	Headers map[string]kiroEventStreamHeader
	Payload []byte
}

// kiroEventStreamDecoder is framing only, not a registered Kiro provider. It
// does not interpret event/error headers, parse JSON, execute tools, or own the
// reader. The caller must arrange cancellation/closing of a blocked reader.
// A single goroutine owns the decoder. Any error, including EOF, is terminal.
type kiroEventStreamDecoder struct {
	reader          io.Reader
	maxMessageBytes int
	terminal        error
}

// maxMessageBytes bounds wire-message size, including both CRCs. Decoded maps
// and strings consume additional memory; this is not a total heap budget.
// It is not the Smithy service-side payload/header size restriction. No separate
// service-side size limits are enforced by this client decoder.
func newKiroEventStreamDecoder(reader io.Reader, maxMessageBytes int) (*kiroEventStreamDecoder, error) {
	if reader == nil || maxMessageBytes < 16 {
		return nil, errKiroEventStreamConfig
	}
	return &kiroEventStreamDecoder{reader: reader, maxMessageBytes: maxMessageBytes}, nil
}

func (d *kiroEventStreamDecoder) next() (kiroEventStreamMessage, error) {
	if d.terminal != nil {
		return kiroEventStreamMessage{}, d.terminal
	}
	message, err := d.readMessage()
	if err != nil {
		d.terminal = err
		return kiroEventStreamMessage{}, err
	}
	return message, nil
}

func (d *kiroEventStreamDecoder) readMessage() (kiroEventStreamMessage, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(d.reader, prelude[:]); err != nil {
		return kiroEventStreamMessage{}, err
	}
	// Check the prelude before trusting lengths or allocating the message body.
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:]) {
		return kiroEventStreamMessage{}, errKiroEventStreamChecksum
	}
	total := binary.BigEndian.Uint32(prelude[:4])
	headersLength := binary.BigEndian.Uint32(prelude[4:8])
	if total < 16 || headersLength > total-16 {
		return kiroEventStreamMessage{}, errKiroEventStreamLength
	}
	if uint64(total) > uint64(d.maxMessageBytes) {
		return kiroEventStreamMessage{}, errKiroEventStreamLimit
	}
	// The budget check also guarantees these uint32-to-int conversions fit on
	// 32-bit platforms. The body contains headers, payload and the trailing CRC.
	body := make([]byte, int(total)-12)
	if _, err := io.ReadFull(d.reader, body); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return kiroEventStreamMessage{}, err
	}
	data := body[:len(body)-4]
	checksum := crc32.Update(crc32.ChecksumIEEE(prelude[:]), crc32.IEEETable, data)
	if checksum != binary.BigEndian.Uint32(body[len(body)-4:]) {
		return kiroEventStreamMessage{}, errKiroEventStreamChecksum
	}
	headers, err := decodeKiroEventStreamHeaders(data[:int(headersLength)])
	if err != nil {
		return kiroEventStreamMessage{}, err
	}
	return kiroEventStreamMessage{Headers: headers, Payload: data[int(headersLength):len(data):len(data)]}, nil
}

func decodeKiroEventStreamHeaders(data []byte) (map[string]kiroEventStreamHeader, error) {
	headers := make(map[string]kiroEventStreamHeader)
	for len(data) > 0 {
		nameLength := int(data[0])
		data = data[1:]
		if nameLength == 0 || len(data) < nameLength+1 || !utf8.Valid(data[:nameLength]) {
			return nil, errKiroEventStreamHeader
		}
		name := string(data[:nameLength])
		if _, duplicate := headers[name]; duplicate {
			return nil, errKiroEventStreamHeader
		}
		kind := data[nameLength]
		data = data[nameLength+1:]
		var value any
		var size int
		switch kind {
		case 0, 1:
			value = kind == 0
		case 2:
			size = 1
		case 3:
			size = 2
		case 4:
			size = 4
		case 5, 8:
			size = 8
		case 6, 7:
			if len(data) < 2 {
				return nil, errKiroEventStreamHeader
			}
			size = int(binary.BigEndian.Uint16(data[:2]))
			data = data[2:]
		case 9:
			size = 16
		default:
			return nil, errKiroEventStreamHeader
		}
		if len(data) < size {
			return nil, errKiroEventStreamHeader
		}
		switch kind {
		case 2:
			value = int8(data[0])
		case 3:
			value = int16(binary.BigEndian.Uint16(data[:2]))
		case 4:
			value = int32(binary.BigEndian.Uint32(data[:4]))
		case 5, 8:
			value = int64(binary.BigEndian.Uint64(data[:8]))
		case 6:
			value = data[:size:size]
		case 7:
			if !utf8.Valid(data[:size]) {
				return nil, errKiroEventStreamHeader
			}
			value = string(data[:size])
		case 9:
			var uuid [16]byte
			copy(uuid[:], data[:16])
			value = uuid
		}
		data = data[size:]
		headers[name] = kiroEventStreamHeader{Type: kind, Value: value}
	}
	return headers, nil
}
