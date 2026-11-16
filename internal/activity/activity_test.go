// SPDX-License-Identifier: AGPL-3.0-or-later

package activity

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundRedactsAndPreservesUTF8(t *testing.T) {
	got, clipped := Bound("API_KEY=private Bearer private-bearer " + strings.Repeat("中", MaxTextBytes))
	if !clipped || len(got) > MaxTextBytes || !utf8.ValidString(got) || strings.Contains(got, "private") {
		t.Fatalf("invalid bounded output: bytes=%d clipped=%v prefix=%q", len(got), clipped, got[:min(len(got), 100)])
	}
	got, clipped = Bound(string([]byte{'a', 0xff, 'b'}))
	if clipped || !utf8.ValidString(got) {
		t.Fatalf("invalid UTF8 output %q", got)
	}
}

func TestInputRedactionAlwaysProducesValidJSON(t *testing.T) {
	args := map[string]any{
		"api_key": "private-quote\"value\\tail",
		"nested":  []any{map[string]any{"password": "nested-private", "text": "Bearer bearer-private"}},
		"command": "echo API_KEY=command-private",
		"count":   9007199254740993,
	}
	got, clipped := Input(args)
	if clipped || !json.Valid(got) || strings.Contains(string(got), "private") || !strings.Contains(string(got), "9007199254740993") {
		t.Fatalf("invalid redacted input %s clipped=%v", got, clipped)
	}
	if args["api_key"] != "private-quote\"value\\tail" {
		t.Fatal("display redaction mutated tool arguments")
	}
	got, clipped = Input(map[string]any{"text": strings.Repeat("中\n\"", MaxTextBytes)})
	if !clipped || len(got) > MaxTextBytes || !json.Valid(got) || !utf8.Valid(got) {
		t.Fatalf("truncated input bytes=%d clipped=%v valid=%v", len(got), clipped, json.Valid(got))
	}
	if got, clipped := Input(map[string]any{"unsupported": make(chan int)}); got != nil || !clipped {
		t.Fatal("unsupported values must not corrupt activity JSON")
	}
}

func TestUpsertBoundsAndSettlesRecords(t *testing.T) {
	var records []Record
	for i := 0; i < MaxRecords+10; i++ {
		records = Upsert(records, Record{ID: fmt.Sprint(i), Status: "running"})
	}
	if len(records) != MaxRecords+1 || records[MaxRecords].Status != "truncated" {
		t.Fatalf("retention count=%d marker=%+v", len(records), records[len(records)-1])
	}
	records = Upsert(records, Record{ID: "0", Status: "success", Output: "finished"})
	if records[0].Status != "success" || len(records) != MaxRecords+1 {
		t.Fatal("retained activity cannot complete after limit")
	}
	for _, status := range []string{"cancelled", "unknown"} {
		settled := Settle(records, status)
		if settled[0].Status != "success" || settled[1].Status != status || settled[MaxRecords].Status != "truncated" || records[1].Status != "running" {
			t.Fatal("settlement changed completed evidence or source")
		}
	}
}
