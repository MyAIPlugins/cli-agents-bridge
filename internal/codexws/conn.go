package codexws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// wsGUID is the RFC 6455 §1.3 magic constant used to derive the accept token.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// rpcPath is the endpoint `codex app-server` serves the JSON-RPC socket on.
// Observed on the wire from the official TUI client (see the spike doc §9.2).
const rpcPath = "/rpc"

// Conn is a client WebSocket connection carrying JSON-RPC messages.
//
// ReadMessage must be called from a single goroutine. WriteMessage is safe to
// call concurrently with ReadMessage: writes are serialised, because the read
// path itself emits pong and close frames.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	wmu       sync.Mutex // serialises every frame write
	closeSent bool       // guarded by wmu

	maxMessageBytes uint64
}

// Dial connects to a codex app-server endpoint and performs the WebSocket
// handshake. Accepted forms: "unix:///path/to.sock" and "ws://host:port".
//
// maxMessageBytes bounds a reassembled message; zero selects
// DefaultMaxMessageBytes.
func Dial(ctx context.Context, endpoint string, maxMessageBytes uint64) (*Conn, error) {
	network, address, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}

	var d net.Dialer
	raw, err := d.DialContext(ctx, network, address)
	if err != nil {
		return nil, fmt.Errorf("codexws: dial %s: %w", endpoint, err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		// Covers the handshake only; cleared once we hand the socket back.
		_ = raw.SetDeadline(deadline)
	}

	c := &Conn{conn: raw, br: bufio.NewReader(raw), maxMessageBytes: maxMessageBytes}
	if err := c.handshake(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return c, nil
}

// parseEndpoint maps an endpoint string onto a net.Dial network/address pair.
func parseEndpoint(endpoint string) (network, address string, err error) {
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		path := strings.TrimPrefix(endpoint, "unix://")
		if path == "" {
			return "", "", fmt.Errorf("codexws: endpoint %q has no socket path", endpoint)
		}
		return "unix", path, nil
	case strings.HasPrefix(endpoint, "ws://"):
		return "tcp", strings.TrimPrefix(endpoint, "ws://"), nil
	default:
		return "", "", fmt.Errorf("codexws: unsupported endpoint %q (want unix:// or ws://)", endpoint)
	}
}

// handshake performs the HTTP Upgrade and verifies the accept token. Verifying
// it is what distinguishes a real WebSocket peer from anything else that
// happens to answer 101.
func (c *Conn) handshake() error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("codexws: handshake nonce: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])

	req := "GET " + rpcPath + " HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n\r\n"
	if _, err := c.conn.Write([]byte(req)); err != nil {
		return fmt.Errorf("codexws: send upgrade: %w", err)
	}

	resp, err := http.ReadResponse(c.br, nil)
	if err != nil {
		return fmt.Errorf("codexws: read upgrade response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("codexws: upgrade rejected: %s", resp.Status)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") {
		return fmt.Errorf("%w: upgrade header %q", ErrProtocol, resp.Header.Get("Upgrade"))
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != acceptToken(key) {
		return fmt.Errorf("%w: bad Sec-WebSocket-Accept", ErrProtocol)
	}
	return nil
}

func acceptToken(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ReadMessage returns the next complete text or binary message, reassembling
// continuation frames. Control frames arriving mid-message are handled inline:
// ping is answered with pong and a close frame ends the connection with
// ErrClosed.
func (c *Conn) ReadMessage() ([]byte, error) {
	limit := c.maxMessageBytes
	if limit == 0 {
		limit = DefaultMaxMessageBytes
	}

	var (
		buf     []byte
		started bool
	)
	for {
		f, err := readFrame(c.br, limit)
		if err != nil {
			return nil, err
		}

		if f.isControl() {
			if err := c.handleControl(f); err != nil {
				return nil, err
			}
			continue
		}

		switch f.opcode {
		case opText, opBinary:
			if started {
				return nil, fmt.Errorf("%w: data frame inside a fragmented message", ErrProtocol)
			}
			started = true
			buf = f.payload
		case opContinuation:
			if !started {
				return nil, fmt.Errorf("%w: continuation without an initial frame", ErrProtocol)
			}
			buf = append(buf, f.payload...)
		default:
			return nil, fmt.Errorf("%w: unknown opcode 0x%x", ErrProtocol, f.opcode)
		}

		// Enforced on the running total, not per frame: many small fragments
		// would otherwise slip past the per-frame check in readFrame.
		if uint64(len(buf)) > limit {
			return nil, fmt.Errorf("%w: reassembled %d bytes", ErrMessageTooLarge, len(buf))
		}
		if f.fin {
			return buf, nil
		}
	}
}

// handleControl answers ping with pong and replies to a peer close before
// reporting ErrClosed.
func (c *Conn) handleControl(f frame) error {
	switch f.opcode {
	case opPing:
		c.wmu.Lock()
		defer c.wmu.Unlock()
		if c.closeSent {
			return nil
		}
		if err := writeFrame(c.conn, opPong, f.payload, true); err != nil {
			return fmt.Errorf("codexws: pong: %w", err)
		}
		return nil
	case opPong:
		return nil
	case opClose:
		c.wmu.Lock()
		if !c.closeSent {
			// Echo the peer's status code, as RFC 6455 §5.5.1 prescribes.
			echo := f.payload
			if len(echo) > maxControlPayload {
				echo = echo[:maxControlPayload]
			}
			_ = writeFrame(c.conn, opClose, echo, true)
			c.closeSent = true
		}
		c.wmu.Unlock()
		return ErrClosed
	default:
		return fmt.Errorf("%w: unknown control opcode 0x%x", ErrProtocol, f.opcode)
	}
}

// WriteMessage sends one text message as a single unfragmented frame.
func (c *Conn) WriteMessage(payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closeSent {
		return ErrClosed
	}
	return writeFrame(c.conn, opText, payload, true)
}

// Close sends a close frame with status 1000 (normal) and closes the socket.
// It is safe to call more than once.
func (c *Conn) Close() error {
	c.wmu.Lock()
	if !c.closeSent {
		// 1000 = normal closure, big-endian, per RFC 6455 §7.4.1.
		_ = writeFrame(c.conn, opClose, []byte{0x03, 0xE8}, true)
		c.closeSent = true
	}
	c.wmu.Unlock()
	return c.conn.Close()
}
