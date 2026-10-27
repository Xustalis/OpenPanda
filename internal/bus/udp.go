// SPDX-License-Identifier: AGPL-3.0-or-later

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
//   kind 3 data       — v2: idlen(1) || peer id || nonce(12) || AESGCM(JSON
//                       Envelope); v1: nonce(12) || AESGCM(body). AAD = the
//                       header through the selector.
//   kind 4 keepalive  — sealed empty body; refreshes the NAT mapping
// STUN binding traffic (RFC 5389, detected by its own magic cookie at bytes
// 4..8) shares the socket: any node answers binding requests, which gives a
// mesh member reflexive-address discovery without an external STUN server.
//
// Who sealed a v2 frame is not a claim the receiver takes on faith: the key is
// derived from the pair's X25519 identities (UDPPairAEAD), so only the named
// peer could have produced the ciphertext. The v1 form keeps the mesh-wide key
// and therefore proves membership only — it survives for peers that publish no
// identity key, and the plane refuses it from any address that does.
//
// One frame type is deliberately outside this: a punch carries its mesh HMAC
// because it exists to open the pinhole before either side has learned the
// other's identity key. It stays gated by the caller's address check, and it
// can only bind an endpoint — it never carries a message.
//
// Replay discipline: sealed datagrams carry no counter — the AEAD is not the
// replay check. The dedup lives one layer up in Core.claimMsgID keyed on the
// envelope's msg_id, the same rule the WS track already relies on. Punch
// frames carry their own freshness timestamp and a per-session nonce.

const (
	udpMagic0 = 'P'
	udpMagic1 = 'N'
	udpVer    = 1
	// udpVerPair marks a frame sealed to one peer pair (see UDPKeySource): the
	// sender's id rides in the clear so the receiver can pick the key before
	// decrypting. udpVer (1) is the legacy mesh-key form, accepted only from a
	// peer that publishes no identity key.
	udpVerPair   = 2
	udpPairIDMax = 128
	udpHdrLen    = 4
	kindPunch    = 1
	kindAck      = 2
	kindData     = 3
	kindKeep     = 4
	udpNonceLen  = 12 // AES-GCM standard nonce
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
// as VerifyHelloP. Freshness alone does not make a frame single-use: a
// captured punch replays cleanly inside MaxHelloAge, so callers gate replays
// through UDPConn.claimPunch.
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

// punchSeenMax bounds the single-use claim table. Every entry got there by
// passing VerifyPunch — only mesh members mint valid signatures — so the cap
// exists against a member spraying distinct signed frames, not the open LAN.
const punchSeenMax = 1024

// UDPConn is a bound UDP socket carrying sealed envelopes and punch frames.
// The callbacks are wired by the core: OnEnvelope gets an authenticated,
// decrypted envelope whose From is proven by the AEAD key; OnPunch gets a
// verified punch frame; OnSTUN is left nil in production (the socket answers
// binding requests itself) and exists for tests.
type UDPConn struct {
	conn   *net.UDPConn
	aead   cipher.AEAD
	secret string
	keys   UDPKeySource
	selfID string
	logger *slog.Logger

	OnEnvelope func(env Envelope, src *net.UDPAddr)
	OnPunch    func(f PunchFrame, src *net.UDPAddr, isAck bool)
	// OnKeepalive fires on a successfully unsealed keepalive datagram. The
	// frame carries no identity — the AEAD open proves only mesh membership —
	// so callers must match src against a known endpoint rather than trusting
	// it standalone.
	OnKeepalive func(src *net.UDPAddr)

	stunMu      sync.Mutex
	stunWaiters map[[stunTxnLen]byte]chan *net.UDPAddr

	stunRateMu sync.Mutex
	stunRate   map[string]*stunRateState

	// punchSeen is the single-use claim table for verified punch frames:
	// a frame's signature stays valid for MaxHelloAge, so without it a
	// captured punch replays cleanly — re-firing session confirmations and
	// ack replies the original sender emitted once. Keyed on the signed
	// fields only, mirroring the hello replay cache.
	punchMu   sync.Mutex
	punchSeen map[string]time.Time
}

// UDPKeySource supplies the per-pair datagram keys and the address-to-peer map
// the receiver needs to refuse a legacy frame from an address that belongs to a
// peer able to seal to the pair. A nil source keeps the plane on the mesh key
// alone, which only proves membership — the behaviour every build before this
// one had, and the only thing available toward a peer that never published an
// identity key.
type UDPKeySource interface {
	// PairKey returns the AEAD shared with peer. False means the peer
	// published no identity key: unknown, or a build that predates them.
	PairKey(peer string) (cipher.AEAD, bool)
	// PeerAtAddr names the peer whose confirmed endpoint is src.
	PeerAtAddr(src *net.UDPAddr) (string, bool)
}

// SetKeySource installs the per-pair key source and this node's own id — the
// id a sealed frame carries as its sender selector. Call it before the read
// loop starts.
func (u *UDPConn) SetKeySource(src UDPKeySource, selfID string) {
	u.keys = src
	u.selfID = selfID
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
		stunRate:    make(map[string]*stunRateState),
		punchSeen:   make(map[string]time.Time),
	}, nil
}

// LocalAddr returns the socket's bound address (tests use it to learn the
// ephemeral port they were given).
func (u *UDPConn) LocalAddr() *net.UDPAddr { return u.conn.LocalAddr().(*net.UDPAddr) }

// Close shuts the socket; an in-flight ReadLoop exits on the read error.
func (u *UDPConn) Close() error { return u.conn.Close() }

// SendEnvelope seals env into one datagram addressed to to, under the pair key
// shared with peer. peer is the datagram's RECIPIENT, which is not the
// envelope's destination whenever a hop forwards the frame: a relay seals to
// the key it shares with the next hop, because that is the node that has to
// open the datagram. Sealing to env.To instead would leave the hop unable to
// read the frame it is meant to relay, so the recipient is passed explicitly.
// An empty peer, or a peer that published no identity key, falls back to the
// mesh key. Oversize frames are refused rather than fragmented — see
// UDPMaxDatagram.
func (u *UDPConn) SendEnvelope(env Envelope, to *net.UDPAddr, peer string) error {
	raw, err := marshalEnvelope(env)
	if err != nil {
		return fmt.Errorf("udp: marshal envelope: %w", err)
	}
	return u.sendSealed(kindData, raw, to, peer)
}

// SendKeepalive emits a sealed empty datagram — proof-of-life that refreshes
// the NAT mapping without carrying a message. peer names the far end so the
// frame seals to the pair like everything else on the plane.
func (u *UDPConn) SendKeepalive(peer string, to *net.UDPAddr) error {
	return u.sendSealed(kindKeep, nil, to, peer)
}

// sendSealed encrypts body under a random nonce and writes one datagram.
//
// peer is the far end whose pair key seals the frame; the selector that rides
// in the clear is this node's own id, because that is what the receiver needs
// to pick the key and to check the envelope's From against. The header and the
// selector are the AAD, so a rewritten id or a swapped version fails the open
// exactly like a tampered body.
func (u *UDPConn) sendSealed(kind byte, body []byte, to *net.UDPAddr, peer string) error {
	aead, pair := u.aead, false
	self := u.selfID
	if u.keys != nil && peer != "" && self != "" {
		if k, ok := u.keys.PairKey(peer); ok {
			aead, pair = k, true
		}
	}
	if pair && len(self) > udpPairIDMax {
		return errors.New("udp: identity exceeds the selector bound")
	}
	nonce := make([]byte, udpNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	hdrLen := udpHdrLen
	if pair {
		hdrLen = udpHdrLen + 1 + len(self)
	}
	if hdrLen+udpNonceLen+len(body)+16 > UDPMaxDatagram {
		return ErrDatagramTooBig
	}
	buf := make([]byte, 0, hdrLen+udpNonceLen+len(body)+16)
	if pair {
		buf = append(buf, udpMagic0, udpMagic1, udpVerPair, kind, byte(len(self)))
		buf = append(buf, self...)
	} else {
		buf = append(buf, udpMagic0, udpMagic1, udpVer, kind)
	}
	hdr := buf[:hdrLen]
	buf = append(buf, nonce...)
	buf = aead.Seal(buf, nonce, body, hdr)
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
		if n < udpHdrLen || d[0] != udpMagic0 || d[1] != udpMagic1 {
			continue
		}
		if d[2] != udpVer && d[2] != udpVerPair {
			continue
		}
		switch d[3] {
		case kindPunch, kindAck:
			// Punch frames stay on the v1 layout and the mesh HMAC: they exist
			// to open a pinhole, which happens before either side has learned
			// the other's identity key, and the handler still holds them to
			// udpPeerTrusted.
			if d[2] != udpVer {
				continue
			}
			body := d[udpHdrLen:]
			var f PunchFrame
			if err := json.Unmarshal(body, &f); err != nil || !VerifyPunch(u.secret, f, time.Now()) {
				u.logger.Debug("udp: bad punch frame", "from", src)
				continue
			}
			if !u.claimPunch(f, time.Now()) {
				u.logger.Debug("udp: replayed punch frame", "from", src)
				continue
			}
			if u.OnPunch != nil {
				u.OnPunch(f, src, d[3] == kindAck)
			}
		case kindData, kindKeep:
			aead, hdr, peer, ok := u.sealTarget(d)
			if !ok {
				u.logger.Debug("udp: no key for datagram", "from", src, "ver", d[2])
				continue
			}
			raw, err := aead.Open(nil, d[len(hdr):len(hdr)+udpNonceLen], d[len(hdr)+udpNonceLen:], hdr)
			if err != nil {
				u.logger.Debug("udp: unseal failed", "from", src, "err", err)
				continue
			}
			if peer == "" {
				if p, refused := u.legacyRefused(src); refused {
					u.logger.Warn("udp: mesh-key frame from a peer that must seal to the pair", "peer", p, "addr", src)
					continue
				}
			}
			if d[3] == kindKeep {
				if u.OnKeepalive != nil {
					u.OnKeepalive(src)
				}
				continue
			}
			var env Envelope
			if err := json.Unmarshal(raw, &env); err != nil {
				u.logger.Debug("udp: bad envelope", "from", src, "err", err)
				continue
			}
			// The pair key proves who sealed the frame. A body naming a
			// different From than the selector never dispatches: it was sealed
			// by someone else, and the identity it claims was not proven.
			if peer != "" && env.From != peer {
				u.logger.Warn("udp: envelope From does not match its sealed selector",
					"selector", peer, "from", env.From)
				continue
			}
			if u.OnEnvelope != nil {
				u.OnEnvelope(env, src)
			}
		}
	}
}

// claimPunch records a verified punch frame's signature as spent. False means
// either an exact replay (same signed fields, still inside the freshness
// window) or a table full of fresh signatures — both are drop conditions:
// the spray re-mints TS per send, so an identical second datagram is never
// the protocol's own doing.
func (u *UDPConn) claimPunch(f PunchFrame, now time.Time) bool {
	key := f.Nonce + "|" + f.From + "|" + strconv.FormatInt(f.TS, 10) + "|" + f.Sig
	u.punchMu.Lock()
	defer u.punchMu.Unlock()
	if _, dup := u.punchSeen[key]; dup {
		return false
	}
	// Prune before the capacity check: entries past MaxHelloAge can never
	// verify again anyway, so a flood window expires out of the table rather
	// than wedging it.
	for k, t := range u.punchSeen {
		if now.Sub(t) > MaxHelloAge {
			delete(u.punchSeen, k)
		}
	}
	if len(u.punchSeen) >= punchSeenMax {
		return false
	}
	u.punchSeen[key] = now
	return true
}

// sealTarget picks the AEAD a received datagram has to open under, the bytes
// its header contributes as AAD, and the peer its selector names (empty for a
// legacy frame).
//
// A pair frame names its sender in the clear; that name is a claim until the
// open succeeds, and the pair key is the only thing that can settle it. A
// legacy frame names nobody, so it is admitted only when src does not belong to
// a peer that could have sealed to the pair — an address that has published an
// identity key must use it, and a mesh-key frame from that address is exactly
// the impersonation this plane refuses.
func (u *UDPConn) sealTarget(d []byte) (cipher.AEAD, []byte, string, bool) {
	switch d[2] {
	case udpVerPair:
		if len(d) < udpHdrLen+1 {
			return nil, nil, "", false
		}
		idLen := int(d[udpHdrLen])
		hdrLen := udpHdrLen + 1 + idLen
		if idLen == 0 || idLen > udpPairIDMax || len(d) < hdrLen+udpNonceLen+16 {
			return nil, nil, "", false
		}
		if u.keys == nil {
			return nil, nil, "", false
		}
		peer := string(d[udpHdrLen+1 : hdrLen])
		aead, ok := u.keys.PairKey(peer)
		if !ok {
			return nil, nil, "", false
		}
		return aead, d[:hdrLen], peer, true
	case udpVer:
		if len(d) < udpHdrLen+udpNonceLen+16 {
			return nil, nil, "", false
		}
		return u.aead, d[:udpHdrLen], "", true
	}
	return nil, nil, "", false
}

// legacyRefused reports whether a mesh-key frame that just opened should be
// dropped because src belongs to a peer that could have sealed to the pair.
// It runs only after the mesh AEAD has accepted the frame, so an unauthenticated
// flood cannot buy an address-table scan.
func (u *UDPConn) legacyRefused(src *net.UDPAddr) (string, bool) {
	if u.keys == nil {
		return "", false
	}
	peer, ok := u.keys.PeerAtAddr(src)
	if !ok {
		return "", false
	}
	if _, hasPair := u.keys.PairKey(peer); hasPair {
		return peer, true
	}
	return "", false
}
