// SPDX-License-Identifier: AGPL-3.0-or-later

// Package util holds small shared helpers.
package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// UUIDv7 returns a UUID per RFC 9562, strictly increasing within this process.
//
// A new millisecond seeds the 74 payload bits from crypto/rand. The payload
// increments within a millisecond (including clock rollback), preserving the
// version and variant bits. Independent processes still have independent
// clocks and seeds: UUID order is not proof of a peer's attempt identity.
func UUIDv7() (string, error) {
	return uuidV7IDs.next(uint64(time.Now().UnixMilli()), rand.Reader)
}

var uuidV7IDs uuidV7Generator

type uuidV7Generator struct {
	mu          sync.Mutex
	initialized bool
	lastMS      uint64
	last        [16]byte
}

const maxUUIDv7MS = uint64(1<<48 - 1)

func (g *uuidV7Generator) next(ms uint64, entropy io.Reader) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	b := g.last
	seed := !g.initialized || ms > g.lastMS
	if !seed {
		ms = g.lastMS
		if !incrementUUIDv7(&b) {
			// Move logical time forward on payload exhaustion, without sleeping
			// or wrapping into an identifier that has already been returned.
			ms++
			seed = true
		}
	}
	if ms > maxUUIDv7MS {
		return "", fmt.Errorf("UUIDv7 timestamp exceeds 48 bits")
	}
	if seed {
		if _, err := io.ReadFull(entropy, b[6:]); err != nil {
			return "", err
		}
		b[6] = (b[6] & 0x0f) | 0x70 // version 7
		b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	}

	// 48-bit timestamp in the top 6 bytes, room for 16 bits below.
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	g.last, g.lastMS, g.initialized = b, ms, true

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:]), nil
}

// incrementUUIDv7 advances only the 74 payload bits, carrying across the
// fixed variant and version fields. False signals payload exhaustion.
func incrementUUIDv7(b *[16]byte) bool {
	for i := 15; i >= 6; i-- {
		mask := byte(0xff)
		if i == 8 {
			mask = 0x3f
		} else if i == 6 {
			mask = 0x0f
		}
		if b[i]&mask != mask {
			b[i]++
			return true
		}
		b[i] &^= mask
	}
	return false
}

// IsUUIDv7 reports whether s is a canonical lowercase UUIDv7 — the shape
// UUIDv7 mints, version nibble pinned — so callers may order-compare only
// ids the timestamp layout actually sorts. An arbitrary foreign string must
// not win "newer" just because it happens to sort high.
func IsUUIDv7(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s[14] != '7' {
		return false
	}
	for i := 0; i < 36; i++ {
		switch i {
		case 8, 13, 18, 23:
			continue
		}
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
