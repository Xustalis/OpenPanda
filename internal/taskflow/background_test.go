// SPDX-License-Identifier: AGPL-3.0-or-later

package taskflow

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	applog "github.com/Xustalis/OpenPanda/internal/log"
)

type backgroundLogWriter chan string

func (w backgroundLogWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

func TestBackgroundAutomationErrorsAreObservableAndRedacted(t *testing.T) {
	s, e, _, _, _ := automationTest(t, true)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_discovery BEFORE INSERT ON task_flow_runs BEGIN SELECT RAISE(ABORT,'password=private-store-value'); END`); err != nil {
		t.Fatal(err)
	}
	e.options.Model = func() AuxiliaryModel { return &auxiliaryTestModel{} }
	e.options.PrivateMemory = func() ([]string, error) { return nil, errors.New("token=private-memory-value") }
	records := make(backgroundLogWriter, 8)
	logger := slog.New(slog.NewJSONHandler(records, nil))
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), applog.CtxKey{}, logger), 3*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); e.loop(ctx, "owner") }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("automation loop did not stop")
		}
	}()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case text := <-records:
			var record struct {
				Message string `json:"msg"`
				Error   string `json:"err"`
			}
			if err := json.Unmarshal([]byte(text), &record); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, "private-store-value") || strings.Contains(text, "private-memory-value") {
				t.Fatal("background diagnostics exposed private error text")
			}
			if !strings.Contains(record.Error, "[redacted]") {
				t.Fatalf("background failure has no sanitized explanation: %s", text)
			}
			seen[record.Message] = true
		case <-ctx.Done():
			t.Fatalf("background error disappeared: %v", seen)
		}
	}
	if !seen["task automation research failed"] || !seen["task automation discovery failed"] {
		t.Fatalf("missing background failure category: %v", seen)
	}
}
