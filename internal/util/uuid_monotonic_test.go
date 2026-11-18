// SPDX-License-Identifier: AGPL-3.0-or-later

package util

import (
	"bytes"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
)

func TestUUIDv7FrozenClockAndRollback(t *testing.T) {
	var g uuidV7Generator
	entropy := bytes.NewReader(make([]byte, 20))
	previous := ""
	for i := range 10000 {
		ms := uint64(100)
		if i%2 == 1 {
			ms = 99 // Wall time can move backward; generation order cannot.
		}
		id, err := g.next(ms, entropy)
		if err != nil {
			t.Fatal(err)
		}
		if !IsUUIDv7(id) || id <= previous || id[:13] != "00000000-0064" {
			t.Fatalf("frozen/rollback order: %q after %q", id, previous)
		}
		previous = id
	}
	id, err := g.next(101, entropy)
	if err != nil || id <= previous || id[:13] != "00000000-0065" {
		t.Fatalf("clock catch-up: id=%q previous=%q err=%v", id, previous, err)
	}
}

func TestUUIDv7CounterCarry(t *testing.T) {
	for _, tc := range []struct {
		name             string
		seed6, seed7     byte
		want6, want7     byte
		wantTimestampHex string
	}{
		{"across-variant", 0, 0, 0x70, 1, "000000000064"},
		{"across-version", 5, 0xff, 0x76, 0, "000000000064"},
		{"payload-exhausted", 0xff, 0xff, 0x70, 0, "000000000065"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := bytes.Repeat([]byte{0xff}, 10)
			seed[0], seed[1] = tc.seed6, tc.seed7
			entropy := bytes.NewReader(append(seed, make([]byte, 10)...))
			var g uuidV7Generator
			first, err := g.next(100, entropy)
			if err != nil {
				t.Fatal(err)
			}
			second, err := g.next(100, entropy)
			if err != nil || second <= first {
				t.Fatalf("carry order: first=%q second=%q err=%v", first, second, err)
			}
			wire := strings.ReplaceAll(second, "-", "")
			b, err := hex.DecodeString(wire)
			if err != nil || wire[:12] != tc.wantTimestampHex || b[6] != tc.want6 || b[7] != tc.want7 || b[8] != 0x80 {
				t.Fatalf("carry changed timestamp/version/variant: %s err=%v", second, err)
			}
			third, err := g.next(100, entropy)
			if err != nil || third <= second {
				t.Fatalf("post-carry order: second=%q third=%q err=%v", second, third, err)
			}
		})
	}
}

func TestUUIDv7EntropyFailureDoesNotAdvanceState(t *testing.T) {
	var g uuidV7Generator
	if id, err := g.next(100, bytes.NewReader(nil)); err == nil || id != "" || g.initialized {
		t.Fatalf("initial entropy failure changed state: id=%q err=%v", id, err)
	}
	first, err := g.next(100, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	last := g.last
	if id, err := g.next(101, bytes.NewReader(nil)); err == nil || id != "" || g.last != last || g.lastMS != 100 {
		t.Fatalf("entropy failure consumed state: id=%q err=%v", id, err)
	}
	second, err := g.next(100, bytes.NewReader(nil))
	if err != nil || second <= first {
		t.Fatalf("same-tick recovery: first=%q second=%q err=%v", first, second, err)
	}
}

func TestUUIDv7TimestampExhaustion(t *testing.T) {
	var g uuidV7Generator
	_, err := g.next(maxUUIDv7MS, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	last := g.last
	if id, err := g.next(maxUUIDv7MS, bytes.NewReader(make([]byte, 10))); err == nil || id != "" || g.last != last || g.lastMS != maxUUIDv7MS {
		t.Fatalf("timestamp exhaustion wrapped state: id=%q err=%v", id, err)
	}
}

func TestUUIDv7ConcurrentFrozenClock(t *testing.T) {
	const workers, perWorker = 16, 256
	var g uuidV7Generator
	entropy := bytes.NewReader(make([]byte, 10))
	ids := make(chan string, workers*perWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			previous := ""
			for range perWorker {
				id, err := g.next(100, entropy)
				if err != nil || id <= previous || !IsUUIDv7(id) {
					t.Errorf("concurrent generation: id=%q previous=%q err=%v", id, previous, err)
					return
				}
				ids <- id
				previous = id
			}
		})
	}
	wg.Wait()
	close(ids)
	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate concurrent UUID: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("generated %d ids, want %d", len(seen), workers*perWorker)
	}
}
