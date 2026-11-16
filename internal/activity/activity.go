// SPDX-License-Identifier: AGPL-3.0-or-later

// Package activity contains display-only evidence, never model replay.
package activity

import (
	"bytes"
	"encoding/json"
	"github.com/Xustalis/OpenPanda/internal/security"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxRecords = 512
const MaxTextBytes = 32768

type Record struct {
	ID         string          `json:"id"`
	Sequence   int             `json:"sequence"`
	Round      int             `json:"round"`
	ToolID     string          `json:"tool_id,omitempty"`
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	StartedAt  int64           `json:"started_at"`
	FinishedAt int64           `json:"finished_at,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
}

func Bound(s string) (string, bool) {
	s = strings.ToValidUTF8(s, "�")
	s = security.Redact(s)
	return boundText(s)
}

func boundText(s string) (string, bool) {
	if len(s) <= MaxTextBytes {
		return s, false
	}
	s = s[:MaxTextBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

func Input(args map[string]any) (json.RawMessage, bool) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, true
	}
	// Redact parsed values, then encode again: redacting raw JSON can leave
	// escaped quotes or backslashes behind and produce an invalid payload.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, true
	}
	data, _ = json.Marshal(redactValue(value))
	s, clipped := boundText(string(data))
	if clipped {
		b, _ := json.Marshal(s)
		for len(b) > MaxTextBytes {
			s = s[:len(s)*3/4]
			for !utf8.ValidString(s) {
				s = s[:len(s)-1]
			}
			b, _ = json.Marshal(s)
		}
		return b, true
	}
	return json.RawMessage(s), false
}

var secretKey = regexp.MustCompile("(?i)(api[_-]?key|apikey|secret|token|password|passwd|credential)")

func redactValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if secretKey.MatchString(key) {
				value[key] = "[redacted]"
			} else {
				value[key] = redactValue(item)
			}
		}
	case []any:
		for i := range value {
			value[i] = redactValue(value[i])
		}
	case string:
		return security.Redact(value)
	}
	return value
}

// Upsert keeps an explicit marker when the retention limit omits evidence.
func Upsert(records []Record, next Record) []Record {
	for i := range records {
		if records[i].ID == next.ID {
			records[i] = next
			return records
		}
	}
	if len(records) >= MaxRecords {
		if records[len(records)-1].Status != "truncated" {
			records = append(records, Record{ID: "history-truncated", Name: "history", Status: "truncated", Truncated: true})
		}
		return records
	}
	return append(records, next)
}

func Settle(records []Record, status string) []Record {
	out := append([]Record(nil), records...)
	for i := range out {
		if out[i].Status == "running" {
			out[i].Status = status
		}
	}
	return out
}
