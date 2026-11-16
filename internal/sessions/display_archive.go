// SPDX-License-Identifier: AGPL-3.0-or-later
package sessions

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/activity"
)

type displayEntry struct {
	Record    *activity.Record `json:"record,omitempty"`
	Reasoning string           `json:"reasoning,omitempty"`
}

func (s *Store) displayPath(id, operation string) string {
	key := sha256.Sum256([]byte(operation))
	return filepath.Join(s.root, "display", id, hex.EncodeToString(key[:])+".jsonl")
}

// AppendDisplay commits display evidence before it can be streamed. This
// append-only archive is independent of bounded operation previews and model
// replay. A generation fence also applies to Side conversations.
func (s *Store) AppendDisplay(id, operation string, record *activity.Record, reasoning string) error {
	unlock, err := lockStore(s.root)
	if err != nil {
		return err
	}
	defer unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, err := s.load(id)
	if err != nil {
		return err
	}
	var current *Operation
	if sess.Operation != nil && sess.Operation.ID == operation && sess.Operation.Status == "running" {
		current = sess.Operation
	}
	for _, chat := range sess.SideChats {
		if chat.Operation != nil && chat.Operation.ID == operation && chat.Operation.Status == "running" {
			current = chat.Operation
		}
	}
	if current == nil {
		return fmt.Errorf("display operation is no longer running")
	}
	file := s.displayPath(id, operation)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(displayEntry{Record: record, Reasoning: reasoning}); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if !current.DisplayArchive {
		current.DisplayArchive = true
		return s.save(sess)
	}
	return nil
}

// A fork owns its copy of completed evidence, so deleting its parent cannot
// erase history from the child. Copy the archive bytes rather than persisting
// an unbounded hydrated transcript in the model replay snapshot.
func (s *Store) forkDisplay(parent, child string, turns []Turn) error {
	seen := make(map[string]bool)
	for _, turn := range turns {
		if turn.OperationID == "" || seen[turn.OperationID] {
			continue
		}
		seen[turn.OperationID] = true
		source, err := os.Open(s.displayPath(parent, turn.OperationID))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		dst := s.displayPath(child, turn.OperationID)
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			_ = source.Close()
			return err
		}
		target, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, err = io.Copy(target, source)
			if err == nil {
				err = target.Sync()
			}
			closeErr := target.Close()
			if err == nil {
				err = closeErr
			}
		}
		_ = source.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// hydrateDisplay is called only for the display read. The persistence and
// replay paths continue to use bounded snapshots, not this archive.
func (s *Store) hydrateDisplay(sess *Session) {
	type evidence struct {
		records []activity.Record
		thought string
	}
	cache := make(map[string]evidence)
	read := func(operation string, preview []activity.Record, thought, status string, expected bool) ([]activity.Record, string) {
		if operation == "" {
			return preview, thought
		}
		e, ok := cache[operation]
		if !ok {
			f, err := os.Open(s.displayPath(sess.ID, operation))
			if os.IsNotExist(err) && !expected {
				return preview, thought
			}
			damaged := err != nil
			if err == nil {
				indexes := make(map[string]int)
				var reasoning strings.Builder
				scan := bufio.NewScanner(f)
				scan.Buffer(make([]byte, 4096), 1<<20)
				for scan.Scan() {
					var item displayEntry
					if json.Unmarshal(scan.Bytes(), &item) != nil {
						damaged = true
						continue
					}
					reasoning.WriteString(item.Reasoning)
					if item.Record != nil {
						record := *item.Record
						if i, ok := indexes[record.ID]; ok {
							e.records[i] = record
						} else {
							indexes[record.ID] = len(e.records)
							e.records = append(e.records, record)
						}
					}
				}
				damaged = damaged || scan.Err() != nil
				e.thought = reasoning.String()
				_ = f.Close()
			}
			if damaged {
				e.records = append(e.records, activity.Record{ID: "archive-unavailable", Name: "history", Status: "unknown", Truncated: true, Output: "Some archived execution records could not be read"})
			}
			cache[operation] = e
		}
		if len(e.records) > 0 {
			seen := make(map[string]bool)
			previews := make(map[string]activity.Record)
			for _, r := range e.records {
				seen[r.ID] = true
			}
			merged := make([]activity.Record, 0, len(preview)+len(e.records))
			for _, r := range preview {
				previews[r.ID] = r
				if !seen[r.ID] && r.ID != "history-truncated" {
					merged = append(merged, r)
				}
			}
			for _, r := range e.records {
				if old, ok := previews[r.ID]; ok && r.Status == "running" && old.Status != "running" {
					r.Status = old.Status
				}
				merged = append(merged, r)
			}
			if status != "running" {
				settle := "unknown"
				if status == "cancelled" {
					settle = "cancelled"
				}
				merged = activity.Settle(merged, settle)
			}
			preview = merged
		}
		if e.thought != "" {
			thought = e.thought
		}
		return preview, thought
	}
	hydrateOp := func(op *Operation) {
		if op != nil {
			op.Activities, op.Thought = read(op.ID, op.Activities, op.Thought, op.Status, op.DisplayArchive)
		}
	}
	hydrateOp(sess.Operation)
	for i := range sess.Turns {
		turn := &sess.Turns[i]
		if turn.Role == "assistant" {
			turn.Activities, turn.Thought = read(turn.OperationID, turn.Activities, turn.Thought, "done", turn.DisplayArchive)
		}
	}
	for i := range sess.SideChats {
		chat := &sess.SideChats[i]
		hydrateOp(chat.Operation)
		for j := range chat.Turns {
			turn := &chat.Turns[j]
			if turn.Role == "assistant" {
				turn.Activities, turn.Thought = read(turn.OperationID, turn.Activities, turn.Thought, "done", turn.DisplayArchive)
			}
		}
	}
}
