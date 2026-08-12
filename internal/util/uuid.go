// Package util holds small shared helpers.
package util

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// UUIDv7 returns a time-ordered 128-bit identifier per RFC 9562.
//
// Layout: 48-bit Unix millisecond timestamp | 0b0111 (version) | 12 random
// bits | 0b10 (variant) | 62 random bits. Values sort lexically by creation
// time, which keeps task rows append-friendly. Uniqueness comes from 74
// random bits, sufficient for the idempotency-key use case in this system.
func UUIDv7() (string, error) {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	// 48-bit timestamp in the top 6 bytes, room for 16 bits below.
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	if _, err := rand.Read(b[6:16]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant

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
