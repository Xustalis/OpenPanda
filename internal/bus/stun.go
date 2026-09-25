package bus

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// stun.go is a minimal RFC 5389 binding implementation — just enough for a
// node to learn its reflexive (NAT-mapped) UDP address and for every node to
// answer binding requests, so a mesh with even one public member is its own
// STUN infrastructure and no external dependency or server is required.
//
// A binding message is a 20-byte header (type, length, magic cookie
// 0x2112A442, 96-bit transaction id) followed by attributes; the response
// carries XOR-MAPPED-ADDRESS (0x0020), where the address bytes are XORed with
// the cookie (v4) or cookie+txn (v6) so a NAT cannot rewrite them blindly.

const (
	stunHdrLen    = 20
	stunTxnLen    = 12
	stunMagic     = 0x2112A442
	stunBindReq   = 0x0001
	stunBindResp  = 0x0101
	attrXORMapped = 0x0020
	stunTimeout   = 3 * time.Second
)

// isSTUN reports whether a datagram is a STUN message: top two type bits must
// be zero (which our 'PN' magic never satisfies) and the cookie must match.
func isSTUN(d []byte) bool {
	return len(d) >= stunHdrLen &&
		d[0]&0xC0 == 0 &&
		binary.BigEndian.Uint32(d[4:8]) == stunMagic
}

// buildBindingRequest returns a binding request and its transaction id.
func buildBindingRequest() (msg []byte, txn [stunTxnLen]byte, err error) {
	if _, err = rand.Read(txn[:]); err != nil {
		return nil, txn, err
	}
	msg = make([]byte, stunHdrLen)
	binary.BigEndian.PutUint16(msg[0:2], stunBindReq)
	// length field stays 0 — no attributes
	binary.BigEndian.PutUint32(msg[4:8], stunMagic)
	copy(msg[8:20], txn[:])
	return msg, txn, nil
}

// buildBindingResponse answers a binding request with an XOR-MAPPED-ADDRESS
// attribute carrying the requester's observed address. Supports v4 and v6.
func buildBindingResponse(req []byte, mapped *net.UDPAddr) ([]byte, error) {
	if len(req) < stunHdrLen || binary.BigEndian.Uint16(req[0:2]) != stunBindReq {
		return nil, errors.New("stun: not a binding request")
	}
	ip4 := mapped.IP.To4()
	var family byte = 0x01
	ipb := mapped.IP.To16()
	if ip4 != nil {
		ipb = ip4
	} else {
		family = 0x02
	}
	attrLen := 4 + len(ipb)
	msg := make([]byte, 0, stunHdrLen+8+attrLen)
	var hdr [stunHdrLen]byte
	binary.BigEndian.PutUint16(hdr[0:2], stunBindResp)
	binary.BigEndian.PutUint16(hdr[2:4], uint16(4+attrLen))
	copy(hdr[4:20], req[4:20]) // cookie + txn echo
	msg = append(msg, hdr[:]...)
	var attr [4]byte
	binary.BigEndian.PutUint16(attr[0:2], attrXORMapped)
	binary.BigEndian.PutUint16(attr[2:4], uint16(attrLen))
	msg = append(msg, attr[:]...)
	msg = append(msg, 0, family)
	var xp [2]byte
	binary.BigEndian.PutUint16(xp[:], uint16(mapped.Port)^uint16(stunMagic>>16))
	msg = append(msg, xp[:]...)
	// XOR the address bytes: v4 with the cookie, v6 with cookie||txn.
	mask := req[4:8]
	if family == 0x02 {
		mask = req[4:20]
	}
	for i, b := range ipb {
		msg = append(msg, b^mask[i])
	}
	return msg, nil
}

// parseBindingResponse extracts the XOR-MAPPED-ADDRESS from a response whose
// transaction id must match the request's — a mismatched txn is somebody
// else's answer.
func parseBindingResponse(msg []byte, txn [stunTxnLen]byte) (*net.UDPAddr, error) {
	if len(msg) < stunHdrLen || binary.BigEndian.Uint16(msg[0:2]) != stunBindResp {
		return nil, errors.New("stun: not a binding response")
	}
	for i := range txn {
		if msg[8+i] != txn[i] {
			return nil, errors.New("stun: transaction id mismatch")
		}
	}
	alen := int(binary.BigEndian.Uint16(msg[2:4]))
	attrs := msg[stunHdrLen:]
	if len(attrs) < alen {
		return nil, errors.New("stun: truncated attributes")
	}
	attrs = attrs[:alen]
	for len(attrs) >= 4 {
		at := binary.BigEndian.Uint16(attrs[0:2])
		l := int(binary.BigEndian.Uint16(attrs[2:4]))
		attrs = attrs[4:]
		if len(attrs) < l {
			return nil, errors.New("stun: truncated attribute")
		}
		val := attrs[:l]
		attrs = attrs[l:]
		if rem := l % 4; rem != 0 && len(attrs) >= 4-rem {
			attrs = attrs[4-rem:] // attributes pad to 4-byte boundaries
		}
		if at != attrXORMapped || len(val) < 4 {
			continue
		}
		port := int(binary.BigEndian.Uint16(val[2:4]) ^ uint16(stunMagic>>16))
		switch val[1] {
		case 0x01: // IPv4
			if len(val) < 8 {
				return nil, errors.New("stun: short v4 address")
			}
			ip := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				ip[i] = val[4+i] ^ msg[4+i] // cookie
			}
			return &net.UDPAddr{IP: ip, Port: port}, nil
		case 0x02: // IPv6
			if len(val) < 20 {
				return nil, errors.New("stun: short v6 address")
			}
			ip := make(net.IP, 16)
			for i := 0; i < 16; i++ {
				ip[i] = val[4+i] ^ msg[4+i] // cookie||txn
			}
			return &net.UDPAddr{IP: ip, Port: port}, nil
		}
	}
	return nil, errors.New("stun: no XOR-MAPPED-ADDRESS")
}

// STUN binding answers are rate-limited per source IP: a request is 20 bytes
// and its answer 32–44, so unthrottled the socket is a ~2x amplifier for
// spoofed-source floods. The cap keeps the reflection surface small without
// hurting real clients — RFC 5389 retransmits on the seconds scale.
const (
	stunRateWindow = time.Second
	stunRateMax    = 10
	// stunRateCap bounds the tracked-source map; a flood from many spoofed
	// sources stops getting answers rather than growing memory unbounded.
	stunRateCap = 4096
)

type stunRateState struct {
	count int
	reset time.Time
}

// allowSTUN reports whether a binding answer may be sent to ip now.
func (u *UDPConn) allowSTUN(ip string) bool {
	now := time.Now()
	u.stunRateMu.Lock()
	defer u.stunRateMu.Unlock()
	st := u.stunRate[ip]
	if st == nil || now.After(st.reset) {
		if st == nil && len(u.stunRate) >= stunRateCap {
			return false
		}
		st = &stunRateState{reset: now.Add(stunRateWindow)}
		u.stunRate[ip] = st
	}
	st.count++
	return st.count <= stunRateMax
}

// handleSTUN answers a binding request directly or wakes a pending binding
// lookup's waiter on a response.
func (u *UDPConn) handleSTUN(d []byte, src *net.UDPAddr) {
	switch binary.BigEndian.Uint16(d[0:2]) {
	case stunBindReq:
		if !u.allowSTUN(src.IP.String()) {
			return
		}
		resp, err := buildBindingResponse(d, src)
		if err != nil {
			return
		}
		_, _ = u.conn.WriteToUDP(resp, src)
	case stunBindResp:
		var txn [stunTxnLen]byte
		copy(txn[:], d[8:20])
		u.stunMu.Lock()
		ch := u.stunWaiters[txn]
		delete(u.stunWaiters, txn)
		u.stunMu.Unlock()
		if ch == nil {
			return
		}
		addr, err := parseBindingResponse(d, txn)
		if err == nil {
			select {
			case ch <- addr:
			default:
			}
		}
	}
}

// STUNBinding asks server (host:port) for our reflexive address on this
// socket. It uses the shared socket — never a fresh one — because the whole
// point is the mapping the socket's outbound packets create.
func (u *UDPConn) STUNBinding(ctx context.Context, server string) (*net.UDPAddr, error) {
	saddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, fmt.Errorf("stun: resolve %s: %w", server, err)
	}
	msg, txn, err := buildBindingRequest()
	if err != nil {
		return nil, err
	}
	ch := make(chan *net.UDPAddr, 1)
	u.stunMu.Lock()
	u.stunWaiters[txn] = ch
	u.stunMu.Unlock()
	defer func() {
		u.stunMu.Lock()
		delete(u.stunWaiters, txn)
		u.stunMu.Unlock()
	}()
	if _, err := u.conn.WriteToUDP(msg, saddr); err != nil {
		return nil, fmt.Errorf("stun: send: %w", err)
	}
	select {
	case addr := <-ch:
		return addr, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(stunTimeout):
		return nil, fmt.Errorf("stun: %s timed out", server)
	}
}
