package ws

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	// defaultMaxFrameSize is the hard ceiling on WebSocket payload size to protect against OOM (issue #806).
	defaultMaxFrameSize = 1 << 20 // 1 MiB
)

var (
	errUnmaskedFrame        = errors.New("ws: client frame must be masked per RFC 6455 §5.1")
	errControlFrameTooLarge = errors.New("ws: control frame payload exceeds 125 bytes")
	errFragmentedControl    = errors.New("ws: control frame must not be fragmented")
)

// readClientFrame reads one masked RFC 6455 frame from a client.
// Per RFC 6455 §5.1, client-to-server frames must always be masked.
func readClientFrame(r *bufio.Reader) ([]byte, byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, 0, err
	}

	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	payloadLen := int64(header[1] & 0x7F)

	// Control frames (close, ping, pong) have opcodes >= 0x8 per RFC 6455 §5.5.
	if opcode >= 0x8 {
		if !fin {
			return nil, opcode, errFragmentedControl
		}
		if payloadLen > 125 {
			return nil, opcode, errControlFrameTooLarge
		}
	}

	// RFC 6455 §5.1: "The server MUST close the connection upon receiving a frame that is not masked."
	if !masked {
		return nil, opcode, errUnmaskedFrame
	}

	switch payloadLen {
	case 126:
		b := make([]byte, 2)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, opcode, err
		}
		payloadLen = int64(binary.BigEndian.Uint16(b))
	case 127:
		b := make([]byte, 8)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, opcode, err
		}
		payloadLen = int64(binary.BigEndian.Uint64(b))
	}

	if payloadLen > defaultMaxFrameSize {
		return nil, opcode, fmt.Errorf("ws: frame payload too large: %d bytes (limit %d)", payloadLen, defaultMaxFrameSize)
	}

	var maskKey [4]byte
	if _, err := io.ReadFull(r, maskKey[:]); err != nil {
		return nil, opcode, err
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, opcode, err
	}

	for i := range payload {
		payload[i] ^= maskKey[i%4]
	}

	return payload, opcode, nil
}

// writeTextFrame encodes payload as a WebSocket text frame (opcode 0x1, FIN set)
// and writes it to bufrw.
func writeTextFrame(bufrw *bufio.ReadWriter, payload []byte) error {
	return writeFrame(bufrw, 0x81, payload)
}

// writePingFrame writes an RFC 6455 ping frame (opcode 0x9, FIN set, empty payload).
func writePingFrame(bufrw *bufio.ReadWriter) error {
	return writeFrame(bufrw, 0x89, nil)
}

// writePongFrame writes an RFC 6455 pong frame (opcode 0xA, FIN set) carrying the given payload.
func writePongFrame(bufrw *bufio.ReadWriter, payload []byte) error {
	return writeFrame(bufrw, 0x8A, payload)
}

// writeCloseFrame writes an RFC 6455 close frame (opcode 0x8) carrying a
// 2-byte status code followed by an optional UTF-8 reason string (issue #224).
func writeCloseFrame(bufrw *bufio.ReadWriter, code uint16, reason string) error {
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, code)
	copy(payload[2:], reason)
	return writeFrame(bufrw, 0x88, payload)
}

// writeFrame writes a single WebSocket frame with the given first-byte opcode and payload.
func writeFrame(bufrw *bufio.ReadWriter, opcode byte, payload []byte) error {
	length := len(payload)
	header := []byte{opcode}

	switch {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126)
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, uint16(length))
		header = append(header, b...)
	default:
		header = append(header, 127)
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(length))
		header = append(header, b...)
	}

	if _, err := bufrw.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := bufrw.Write(payload); err != nil {
			return err
		}
	}
	return bufrw.Flush()
}

// wsWriter provides thread-safe writing of WebSocket frames over a hijacked connection (issue #806).
type wsWriter struct {
	conn  net.Conn
	bufrw *bufio.ReadWriter
	mu    sync.Mutex
}

func newWSWriter(conn net.Conn, bufrw *bufio.ReadWriter) *wsWriter {
	return &wsWriter{conn: conn, bufrw: bufrw}
}

func (w *wsWriter) writeText(payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return writeTextFrame(w.bufrw, payload)
}

func (w *wsWriter) writePing() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return writePingFrame(w.bufrw)
}

func (w *wsWriter) writePong(payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return writePongFrame(w.bufrw, payload)
}

func (w *wsWriter) writeClose(code uint16, reason string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return writeCloseFrame(w.bufrw, code, reason)
}
