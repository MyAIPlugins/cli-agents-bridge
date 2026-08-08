package codexws

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles -----------------------------------------------------------

// fakeConn glues a reader (what the server "sends") to a writer (what we
// capture) behind the net.Conn interface, so read-path tests stay
// deterministic and goroutine-free.
type fakeConn struct {
	io.Reader
	io.Writer
}

func (fakeConn) Close() error                       { return nil }
func (fakeConn) LocalAddr() net.Addr                { return nil }
func (fakeConn) RemoteAddr() net.Addr               { return nil }
func (fakeConn) SetDeadline(t time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// serverFrame builds a server-to-client frame, which per RFC 6455 is never
// masked. writeFrame always masks, so tests need their own encoder.
func serverFrame(opcode byte, payload []byte, fin bool) []byte {
	b0 := opcode
	if fin {
		b0 |= finBit
	}
	out := []byte{b0}
	n := len(payload)
	switch {
	case n < 126:
		out = append(out, byte(n))
	case n < 1<<16:
		out = append(out, 126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	default:
		out = append(out, 127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	return append(out, payload...)
}

func newTestConn(serverBytes []byte) (*Conn, *bytes.Buffer) {
	sent := &bytes.Buffer{}
	fc := fakeConn{Reader: bytes.NewReader(serverBytes), Writer: sent}
	return &Conn{conn: fc, br: bufio.NewReader(fc)}, sent
}

// firstClientFrame decodes the first frame we wrote back to the server.
// Client frames are masked, so it unmasks before returning the payload.
func firstClientFrame(t *testing.T, b []byte) frame {
	t.Helper()
	f, err := readFrame(bytes.NewReader(b), 0)
	require.NoError(t, err)
	return f
}

// --- frame round-trip -------------------------------------------------------

func TestFrame_RoundTripAcrossLengthEncodings(t *testing.T) {
	// The three payload-length encodings (7-bit, 16-bit, 64-bit) are where
	// hand-rolled codecs typically break.
	cases := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"tiny", 5},
		{"boundary_125", 125},
		{"boundary_126_uses_uint16", 126},
		{"boundary_65535", 65535},
		{"over_64KiB_uses_uint64", 100_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := bytes.Repeat([]byte("x"), tc.size)
			var buf bytes.Buffer
			require.NoError(t, writeFrame(&buf, opText, payload, true))

			got, err := readFrame(bytes.NewReader(buf.Bytes()), 0)
			require.NoError(t, err)
			assert.True(t, got.fin)
			assert.Equal(t, opText, got.opcode)
			assert.Equal(t, payload, got.payload)
		})
	}
}

func TestFrame_ClientFramesAreMaskedWithVaryingKeys(t *testing.T) {
	payload := []byte("same payload twice")
	var a, b bytes.Buffer
	require.NoError(t, writeFrame(&a, opText, payload, true))
	require.NoError(t, writeFrame(&b, opText, payload, true))

	assert.Equal(t, maskBit, a.Bytes()[1]&maskBit, "client frame must set the mask bit")
	assert.NotEqual(t, a.Bytes(), b.Bytes(), "a fresh masking key is required per frame")
}

// --- fragmentation ----------------------------------------------------------

func TestReadMessage_ReassemblesFragments(t *testing.T) {
	stream := bytes.Join([][]byte{
		serverFrame(opText, []byte("part-one "), false),
		serverFrame(opContinuation, []byte("part-two "), false),
		serverFrame(opContinuation, []byte("part-three"), true),
	}, nil)

	c, _ := newTestConn(stream)
	msg, err := c.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "part-one part-two part-three", string(msg))
}

func TestReadMessage_ReassemblesLargeFragmentedPayload(t *testing.T) {
	// A 14 KiB CRI verdict arriving in chunks is the real-world case; 300 KiB
	// across 3 fragments also exercises the 64-bit length path.
	chunk := bytes.Repeat([]byte("A"), 100_000)
	stream := bytes.Join([][]byte{
		serverFrame(opText, chunk, false),
		serverFrame(opContinuation, chunk, false),
		serverFrame(opContinuation, chunk, true),
	}, nil)

	c, _ := newTestConn(stream)
	msg, err := c.ReadMessage()
	require.NoError(t, err)
	assert.Len(t, msg, 300_000)
}

// --- control frames interleaved --------------------------------------------

func TestReadMessage_AnswersPingInsideFragmentedMessage(t *testing.T) {
	// A ping between fragments must be answered without corrupting the message
	// being reassembled: if we stayed silent the server would drop us.
	stream := bytes.Join([][]byte{
		serverFrame(opText, []byte("before "), false),
		serverFrame(opPing, []byte("keepalive"), true),
		serverFrame(opContinuation, []byte("after"), true),
	}, nil)

	c, sent := newTestConn(stream)
	msg, err := c.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "before after", string(msg))

	pong := firstClientFrame(t, sent.Bytes())
	assert.Equal(t, opPong, pong.opcode, "ping must be answered with pong")
	assert.Equal(t, []byte("keepalive"), pong.payload, "pong must echo the ping payload")
}

func TestReadMessage_IgnoresPong(t *testing.T) {
	stream := bytes.Join([][]byte{
		serverFrame(opPong, []byte("unsolicited"), true),
		serverFrame(opText, []byte("payload"), true),
	}, nil)

	c, sent := newTestConn(stream)
	msg, err := c.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, "payload", string(msg))
	assert.Empty(t, sent.Bytes(), "a pong needs no reply")
}

func TestReadMessage_ClosePeerInitiated(t *testing.T) {
	status := []byte{0x03, 0xE8} // 1000 normal closure
	c, sent := newTestConn(serverFrame(opClose, status, true))

	_, err := c.ReadMessage()
	assert.ErrorIs(t, err, ErrClosed)

	reply := firstClientFrame(t, sent.Bytes())
	assert.Equal(t, opClose, reply.opcode)
	assert.Equal(t, status, reply.payload, "close status must be echoed")
}

func TestClose_SendsNormalClosureOnce(t *testing.T) {
	c, sent := newTestConn(nil)
	require.NoError(t, c.Close())

	f := firstClientFrame(t, sent.Bytes())
	assert.Equal(t, opClose, f.opcode)
	assert.Equal(t, []byte{0x03, 0xE8}, f.payload)

	before := sent.Len()
	require.NoError(t, c.Close())
	assert.Equal(t, before, sent.Len(), "second Close must not emit a second frame")

	assert.ErrorIs(t, c.WriteMessage([]byte("late")), ErrClosed)
}

// --- protocol violations ----------------------------------------------------

func TestReadMessage_ProtocolViolations(t *testing.T) {
	fragmentedControl := serverFrame(opPing, []byte("x"), false)
	oversizedControl := serverFrame(opPing, bytes.Repeat([]byte("x"), 126), true)

	reserved := serverFrame(opText, []byte("x"), true)
	reserved[0] |= 0x40 // set RSV1 without having negotiated an extension

	cases := []struct {
		name   string
		stream []byte
	}{
		{"fragmented_control_frame", fragmentedControl},
		{"control_payload_over_125", oversizedControl},
		{"reserved_bit_set", reserved},
		{"continuation_without_start", serverFrame(opContinuation, []byte("orphan"), true)},
		{"data_frame_inside_fragmented_message", bytes.Join([][]byte{
			serverFrame(opText, []byte("first"), false),
			serverFrame(opText, []byte("second"), true),
		}, nil)},
		{"unknown_opcode", serverFrame(0x3, []byte("x"), true)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestConn(tc.stream)
			_, err := c.ReadMessage()
			assert.ErrorIs(t, err, ErrProtocol)
		})
	}
}

func TestReadMessage_RejectsOversizedMessage(t *testing.T) {
	t.Run("single_frame_over_limit", func(t *testing.T) {
		c, _ := newTestConn(serverFrame(opText, bytes.Repeat([]byte("x"), 200), true))
		c.maxMessageBytes = 100
		_, err := c.ReadMessage()
		assert.ErrorIs(t, err, ErrMessageTooLarge)
	})

	t.Run("fragments_summing_over_limit", func(t *testing.T) {
		// Each fragment is under the limit; only the running total exceeds it.
		chunk := bytes.Repeat([]byte("x"), 60)
		c, _ := newTestConn(bytes.Join([][]byte{
			serverFrame(opText, chunk, false),
			serverFrame(opContinuation, chunk, true),
		}, nil))
		c.maxMessageBytes = 100
		_, err := c.ReadMessage()
		assert.ErrorIs(t, err, ErrMessageTooLarge)
	})
}

func TestReadMessage_TruncatedStreamReturnsEOF(t *testing.T) {
	full := serverFrame(opText, []byte("complete message"), true)
	c, _ := newTestConn(full[:4]) // header plus a couple of payload bytes
	_, err := c.ReadMessage()
	assert.True(t, errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF), "got %v", err)
}

// --- endpoint parsing -------------------------------------------------------

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		name        string
		endpoint    string
		wantNetwork string
		wantAddress string
		wantErr     bool
	}{
		{"unix_absolute", "unix:///Users/alan/.codex/as.sock", "unix", "/Users/alan/.codex/as.sock", false},
		{"ws_loopback", "ws://127.0.0.1:4500", "tcp", "127.0.0.1:4500", false},
		{"unix_without_path", "unix://", "", "", true},
		{"unsupported_scheme", "https://example.com", "", "", true},
		{"empty", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			network, address, err := parseEndpoint(tc.endpoint)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantNetwork, network)
			assert.Equal(t, tc.wantAddress, address)
		})
	}
}

// --- handshake --------------------------------------------------------------

// handshakeServer answers one upgrade request over a net.Pipe, letting the
// caller corrupt the response to test rejection paths.
func handshakeServer(t *testing.T, srv net.Conn, mutate func(key string) string) {
	t.Helper()
	go func() {
		defer func() { _ = srv.Close() }()
		br := bufio.NewReader(srv)
		var key string
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.ToLower(line), "sec-websocket-key:") {
				key = strings.TrimSpace(line[len("sec-websocket-key:"):])
			}
			if line == "\r\n" {
				break
			}
		}
		_, _ = srv.Write([]byte(mutate(key)))
	}()
}

func okResponse(key string) string {
	return "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
		"Sec-WebSocket-Accept: " + acceptToken(key) + "\r\n\r\n"
}

func TestHandshake_Accepts101WithValidToken(t *testing.T) {
	cli, srv := net.Pipe()
	handshakeServer(t, srv, okResponse)

	c := &Conn{conn: cli, br: bufio.NewReader(cli)}
	assert.NoError(t, c.handshake())
}

func TestHandshake_Rejections(t *testing.T) {
	cases := []struct {
		name     string
		response func(key string) string
	}{
		{"non_101_status", func(string) string {
			return "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n"
		}},
		{"wrong_accept_token", func(string) string {
			bogus := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("z"), 20))
			return "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" +
				"Sec-WebSocket-Accept: " + bogus + "\r\n\r\n"
		}},
		{"missing_upgrade_header", func(key string) string {
			return "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\n" +
				"Sec-WebSocket-Accept: " + acceptToken(key) + "\r\n\r\n"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli, srv := net.Pipe()
			handshakeServer(t, srv, tc.response)

			c := &Conn{conn: cli, br: bufio.NewReader(cli)}
			assert.Error(t, c.handshake())
		})
	}
}

func TestAcceptToken_MatchesRFC6455Example(t *testing.T) {
	// RFC 6455 §1.3 worked example — pins our SHA1+base64 derivation to the spec.
	assert.Equal(t, "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", acceptToken("dGhlIHNhbXBsZSBub25jZQ=="))
}

func TestDial_RejectsUnsupportedEndpoint(t *testing.T) {
	_, err := Dial(context.Background(), "http://example.com", 0)
	assert.Error(t, err)
}
