// Package codexws implements the client half of RFC 6455 needed to speak
// JSON-RPC to `codex app-server --listen unix://<path>`.
//
// Scope is deliberately narrow: text messages, client-side masking, ping/pong
// and a clean close handshake. No server role, no permessage-deflate, no
// extensions, no subprotocol negotiation. Codex marks its app-server
// "experimental and unsupported", so this package is isolated on purpose: if
// the vendor breaks the contract it can be deleted without touching anything
// else. See docs/spike-codex-app-server.md.
package codexws

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// RFC 6455 §5.2 opcodes — only the subset a JSON-RPC client needs.
const (
	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xA
)

const (
	finBit  byte = 0x80
	rsvBits byte = 0x70
	maskBit byte = 0x80

	// maxControlPayload is the RFC 6455 §5.5 hard limit: control frames carry
	// at most 125 bytes and must never be fragmented.
	maxControlPayload = 125

	// DefaultMaxMessageBytes bounds a reassembled message. A hostile or
	// malfunctioning peer could otherwise announce a 2^64 payload and drive us
	// into an unbounded allocation.
	DefaultMaxMessageBytes = 16 << 20
)

// Protocol-level errors. Sentinels let callers branch with errors.Is instead
// of matching strings.
var (
	ErrProtocol        = errors.New("codexws: protocol violation")
	ErrMessageTooLarge = errors.New("codexws: message exceeds max size")
	ErrClosed          = errors.New("codexws: connection closed")
)

// frame is a single RFC 6455 frame, already unmasked.
type frame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// isControl reports whether the frame is a control frame (opcode high bit set).
// Control frames may be interleaved between the fragments of a data message,
// which is why readMessage has to handle them mid-stream.
func (f frame) isControl() bool { return f.opcode&0x8 != 0 }

// readFrame reads one frame. A zero maxPayload means DefaultMaxMessageBytes.
// io.EOF is returned unwrapped so callers can distinguish a clean stream end.
func readFrame(r io.Reader, maxPayload uint64) (frame, error) {
	if maxPayload == 0 {
		maxPayload = DefaultMaxMessageBytes
	}

	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return frame{}, err
	}

	f := frame{fin: head[0]&finBit != 0, opcode: head[0] & 0x0f}
	if head[0]&rsvBits != 0 {
		// We never negotiate an extension, so a reserved bit means the peer is
		// speaking something we did not agree to.
		return frame{}, fmt.Errorf("%w: reserved bits set", ErrProtocol)
	}

	masked := head[1]&maskBit != 0
	size := uint64(head[1] & 0x7f)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return frame{}, err
		}
		size = binary.BigEndian.Uint64(ext[:])
	}

	if f.isControl() {
		if !f.fin {
			return frame{}, fmt.Errorf("%w: fragmented control frame", ErrProtocol)
		}
		if size > maxControlPayload {
			return frame{}, fmt.Errorf("%w: control frame payload %d > %d", ErrProtocol, size, maxControlPayload)
		}
	}
	if size > maxPayload {
		return frame{}, fmt.Errorf("%w: frame announces %d bytes", ErrMessageTooLarge, size)
	}

	var key [4]byte
	if masked {
		if _, err := io.ReadFull(r, key[:]); err != nil {
			return frame{}, err
		}
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame{}, err
	}
	if masked {
		applyMask(payload, key)
	}
	f.payload = payload
	return f, nil
}

// writeFrame writes one frame. Client-to-server frames are always masked with
// a fresh random key (RFC 6455 §5.3 requires an unpredictable key per frame).
func writeFrame(w io.Writer, opcode byte, payload []byte, fin bool) error {
	var key [4]byte
	if _, err := rand.Read(key[:]); err != nil {
		return fmt.Errorf("codexws: masking key: %w", err)
	}

	size := len(payload)
	header := make([]byte, 0, 14)
	b0 := opcode
	if fin {
		b0 |= finBit
	}
	header = append(header, b0)

	switch {
	case size < 126:
		header = append(header, maskBit|byte(size))
	case size < 1<<16:
		header = append(header, maskBit|126)
		header = binary.BigEndian.AppendUint16(header, uint16(size))
	default:
		header = append(header, maskBit|127)
		header = binary.BigEndian.AppendUint64(header, uint64(size))
	}
	header = append(header, key[:]...)

	masked := make([]byte, size)
	copy(masked, payload)
	applyMask(masked, key)

	// One Write for header+payload: a partial write between the two would
	// desynchronise the peer's framing beyond recovery.
	if _, err := w.Write(append(header, masked...)); err != nil {
		return fmt.Errorf("codexws: write frame: %w", err)
	}
	return nil
}

func applyMask(b []byte, key [4]byte) {
	for i := range b {
		b[i] ^= key[i%4]
	}
}
