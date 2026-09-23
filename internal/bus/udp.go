package bus

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// udp.go is the farsky datagram plane (whitepaper §9.2): a UDP transport for
// links where a TCP/WebSocket session cannot exist — NAT-bound peers punching
// a pinhole, or lossy high-latency paths where the kernel's TCP machinery is
// the thing that fails. Every datagram is either an HMAC-signed punch frame
// (the pinhole opener, small enough to spray) or an AES-256-GCM-sealed JSON
// envelope, so the plane carries both confidentiality and integrity where the
// WS track relies on the connection's hello handshake alone.
//
// Datagram layout:  [ 'P' 'N' ] [ver] [kind] [body]
//   kind 1 punch      — JSON PunchFrame, HMAC-signed proof of mesh membership
//   kind 2 punch_ack  — same body; answers a punch to confirm the path is up
//   kind 3 data       — nonce(12) || AESGCM(JSON Envelope); AAD = 4-byte header
//   kind 4 keepalive  — sealed empty body; refreshes the NAT mapping
// STUN binding traffic (RFC 5389, detected by its own magic cookie at bytes
// 4..8) shares the socket: any node answers binding requests, which gives a
// mesh member reflexive-address discovery without an external STUN server.
//
// Replay discipline: sealed datagrams carry no counter — the AEAD is not the
// replay check. The dedup lives one layer up in Core.claimMsgID keyed on the
// envelope's msg_id, the same rule the WS track already relies on. Punch
// frames carry their own freshness timestamp and a per-session nonce.

const (
	udpMagic0   = 'P'
	udpMagic1   = 'N'
	udpVer      = 1
	udpHdrLen   = 4
	kindPunch   = 1
	kindAck     = 2
	kindData    = 3
	kindKeep    = 4
	udpNonceLen = 12 // AES-GCM standard nonce
)

// UDPMaxDatagram is the largest datagram this plane emits: 1400 bytes stays
// inside a conservative path MTU on both v4 and v6 (tunnels, PPPoE) so the
// sender never has to learn fragmentation. A caller whose envelope does not
// fit falls back to the WS track or parks the message in the DTN outbox —
// datagrams are for signaling and small control traffic, not artifacts.
const UDPMaxDatagram = 1400

// ErrDatagramTooBig is returned when an envelope cannot fit inside one
// datagram; the sender should use the WS track or store-and-forward instead.
var ErrDatagramTooBig = errors.New("udp: envelope exceeds datagram bound")

// PunchFrame is the pinhole opener: proof of mesh membership small enough to
// spray at every candidate endpoint. Sig is HMAC-SHA256 (hex) of
// "punch|<nonce>|<from>|<ts>" under the mesh secret — the same membership
// proof a hello carries, bound to a session nonce so a captured frame cannot
// open a pinhole for a different handshake. TS bounds replay to MaxHelloAge.
type PunchFrame struct {
	Nonce string `json:"n"`
	From  string `json:"f"`
	TS    int64  `json:"t"`
	Sig   string `json:"s"`
}

// PunchSig computes the punch frame signature.
func PunchSig(secret, nonce, from string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("punch|" + nonce + "|" + from + "|" + strconv.FormatInt(ts, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyPunch reports whether f is a fresh, correctly-signed punch frame.
// Fail-closed on empty secret, empty sig, or stale timestamp — same posture
// as VerifyHelloP.
func VerifyPunch(secret string, f PunchFrame, now time.Time) bool {
	if secret == "" || f.Sig == "" || f.From == "" || f.Nonce == "" {
		return false
	}
	age := now.Sub(time.Unix(f.TS, 0))
	if age > MaxHelloAge || age < -MaxHelloAge {
		return false
	}
	return hmac.Equal([]byte(PunchSig(secret, f.Nonce, f.From, f.TS)), []byte(f.Sig))
}

// UDPConn is a bound UDP socket carrying sealed envelopes and punch frames.
// The callbacks are wired by the core: OnEnvelope gets an authenticated,
// decrypted envelope whose From is proven by the AEAD key; OnPunch gets a
// verified punch frame; OnSTUN is left nil in production (the socket answers
// binding requests itself) and exists for tests.
type UDPConn struct {
	conn   *net.UDPConn
	aead   cipher.AEAD
	secret string
	logger *slog.Logger

	OnEnvelope func(env Envelope, src *net.UDPAddr)
	OnPunch    func(f PunchFrame, src *net.UDPAddr, isAck bool)

	stunMu      sync.Mutex
	stunWaiters map[[stunTxnLen]byte]chan *net.UDPAddr
}

// ListenUDP binds addr (e.g. ":7836") and returns a plane ready to ReadLoop.
// The AEAD key is derived from the mesh secret under a domain separator so a
// datagram key is never the same bytes as the hello HMAC key.
func ListenUDP(addr, secret string, logger *slog.Logger) (*UDPConn, error) {
	if logger == nil {
		logger = slog.Default()
	}
	uaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("udp: resolve %s: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", uaddr)
	if err != nil {
		return nil, fmt.Errorf("udp: listen %s: %w", addr, err)
	}
	key := sha256.Sum256(append([]byte("panda-udp-aead-v1\x00"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		conn.Close()
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &UDPConn{
		conn: conn, aead: aead, secret: secret, logger: logger,
		stunWaiters: make(map[[stunTxnLen]byte]chan *net.UDPAddr),
	}, nil
}

// LocalAddr returns the socket's bound address (tests use it to learn the
// ephemeral port they were given).
func (u *UDPConn) LocalAddr() *net.UDPAddr { return u.conn.LocalAddr().(*net.UDPAddr) }

// Close shuts the socket; an in-flight ReadLoop exits on the read error.
func (u *UDPConn) Close() error { return u.conn.Close() }

// SendEnvelope seals env into one datagram addressed to src. Oversize frames
// are refused rather than fragmented — see UDPMaxDatagram.
func (u *UDPConn) SendEnvelope(env Envelope, to *net.UDPAddr) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("udp: marshal envelope: %w", err)
	}
	if len(raw)+udpHdrLen+udpNonceLen+16 > UDPMaxDatagram {
		return ErrDatagramTooBig
	}
	return u.sendSealed(kindData, raw, to)
}

// SendKeepalive emits a sealed empty datagram — proof-of-life that refreshes
// the NAT mapping without carrying a message.
func (u *UDPConn) SendKeepalive(to *net.UDPAddr) error {
	return u.sendSealed(kindKeep, nil, to)
}

// sendSealed encrypts body under a random nonce and writes one datagram.
func (u *UDPConn) sendSealed(kind byte, body []byte, to *net.UDPAddr) error {
	nonce := make([]byte, udpNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	buf := make([]byte, 0, udpHdrLen+udpNonceLen+len(body)+16)
	buf = append(buf, udpMagic0, udpMagic1, udpVer, kind)
	buf = append(buf, nonce...)
	hdr := buf[:udpHdrLen]
	buf = u.aead.Seal(buf, nonce, body, hdr)
	_, err := u.conn.WriteToUDP(buf, to)
	return err
}

// SendPunch writes an unsigned-size punch frame (its own HMAC is the auth).
// isAck selects the ack kind so the receiver can distinguish an opener from
// the path-confirmation reply.
func (u *UDPConn) SendPunch(f PunchFrame, to *net.UDPAddr, isAck bool) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	kind := byte(kindPunch)
	if isAck {
		kind = kindAck
	}
	buf := make([]byte, 0, udpHdrLen+len(body))
	buf = append(buf, udpMagic0, udpMagic1, udpVer, kind)
	buf = append(buf, body...)
	if len(buf) > UDPMaxDatagram {
		return ErrDatagramTooBig
	}
	_, err = u.conn.WriteToUDP(buf, to)
	return err
}

// ReadLoop reads datagrams until ctx is done or the socket closes. Each
// datagram is classified: STUN traffic goes to the STUN handler, PN frames
// are dispatched by kind. Malformed or unverifiable input is dropped without
// a reply — the same fail-closed posture as the WS track's pre-hello gate.
func (u *UDPConn) ReadLoop(ctx context.Context) {
	buf := make([]byte, 64<<10)
	for {
		if ctx.Err() != nil {
			return
		}
		n, src, err := u.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				u.logger.Debug("udp: read", "err", err)
			}
			return
		}
		d := buf[:n]
		if len(d) >= stunHdrLen && isSTUN(d) {
			u.handleSTUN(d, src)
			continue
		}
		if n < udpHdrLen || d[0] != udpMagic0 || d[1] != udpMagic1 || d[2] != udpVer {
			continue
		}
		body := d[udpHdrLen:]
		switch d[3] {
		case kindPunch, kindAck:
			var f PunchFrame
			if err := json.Unmarshal(body, &f); err != nil || !VerifyPunch(u.secret, f, time.Now()) {
				u.logger.Debug("udp: bad punch frame", "from", src)
				continue
			}
			if u.OnPunch != nil {
				u.OnPunch(f, src, d[3] == kindAck)
			}
		case kindData:
			raw, err := u.open(body, d[:udpHdrLen])
			if err != nil {
				u.logger.Debug("udp: unseal failed", "from", src, "err", err)
				continue
			}
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				u.logger.Debug("udp: bad envelope", "from", src, "err", err)
				continue
			}
			if u.OnEnvelope != nil {
				u.OnEnvelope(env, src)
			}
		case kindKeep:
			if _, err := u.open(body, d[:udpHdrLen]); err != nil {
				u.logger.Debug("udp: unseal keepalive failed", "from", src)
			}
		}
	}
}

// open decrypts a sealed datagram body (nonce || ciphertext) with the frame
// header as associated data — a tampered header fails the same way a tampered
// body does.
func (u *UDPConn) open(body, hdr []byte) ([]byte, error) {
	if len(body) < udpNonceLen+16 {
		return nil, errors.New("udp: short sealed body")
	}
	return u.aead.Open(nil, body[:udpNonceLen], body[udpNonceLen:], hdr)
}
