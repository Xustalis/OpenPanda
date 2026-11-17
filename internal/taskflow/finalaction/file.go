// SPDX-License-Identifier: AGPL-3.0-or-later

// Package finalaction provides concrete, enforceable side effect adapters.
// A caller must durably record dispatching and hold current authority before
// invoking Publish. Reconcile only observes facts and never repeats an action.
package finalaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/util"
)

type FileIntent struct {
	ActionID           string `json:"actionId"`
	RequestID          string `json:"requestId"`
	AttemptID          string `json:"attemptId"`
	AccountID          string `json:"accountId"`
	DeviceID           string `json:"deviceId"`
	InputRevision      string `json:"inputRevision"`
	AssignmentRevision string `json:"assignmentRevision"`
	Generation         string `json:"automationGeneration"`
	Directory          string `json:"directory"`
	DirectoryIdentity  string `json:"directoryIdentity"`
	Filename           string `json:"filename"`
	Content            string `json:"content"`
	SHA256             string `json:"sha256"`
	ExpiresAt          int64  `json:"expiresAt"`
}
type Receipt struct {
	ActionID string `json:"actionId"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Method   string `json:"method"`
}

func Hash(b []byte) string               { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (i FileIntent) Fingerprint() string { b, _ := json.Marshal(i); return Hash(b) }

func ValidateDestination(directory, name string) error {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, "/\\:\x00") || strings.HasPrefix(name, ".panda-action-") || len(name) > 200 {
		return errors.New("file publication requires an absolute directory and one local filename")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	if resolved != directory {
		return errors.New("publication directory must be a canonical path without symlinks")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("publication destination is not a directory")
	}
	return nil
}
func (i FileIntent) Validate() error {
	if !util.IsUUIDv7(i.ActionID) || i.RequestID == "" || i.AttemptID == "" || i.AccountID == "" || i.DeviceID == "" || i.InputRevision == "" || i.AssignmentRevision == "" || i.Generation == "" || i.ExpiresAt <= 0 || len(i.Content) == 0 || len(i.Content) > 1<<20 || Hash([]byte(i.Content)) != i.SHA256 {
		return errors.New("incomplete or changed file publication intent")
	}
	if i.DirectoryIdentity == "" {
		return errors.New("publication directory identity required")
	}
	return ValidateDestination(i.Directory, i.Filename)
}

// DirectoryIdentity pins the directory object selected by local policy. A
// replaced path cannot inherit consent for a previously prepared destination.
func DirectoryIdentity(directory string) (string, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	return rootIdentity(root)
}
func checkRoot(root *os.Root, i FileIntent) error {
	id, err := rootIdentity(root)
	if err != nil {
		return err
	}
	if id != i.DirectoryIdentity {
		return errors.New("publication directory changed")
	}
	return nil
}
func stageName(i FileIntent) string { return ".panda-action-" + i.ActionID + ".prepared" }

func publicationAllowed(ctx context.Context, i FileIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if i.ExpiresAt <= time.Now().UnixMilli() {
		return errors.New("file publication intent expired")
	}
	return nil
}

// Publish creates a complete new file by hard linking a flushed staging file.
// Link cannot replace an existing target. The staging link remains as provenance
// for read-only crash reconciliation; a same-content pre-existing file is not
// mistaken for a successful action.
func Publish(ctx context.Context, i FileIntent, checkpoint func(context.Context) error) (Receipt, error) {
	if err := i.Validate(); err != nil {
		return Receipt{}, err
	}
	if err := publicationAllowed(ctx, i); err != nil {
		return Receipt{}, err
	}
	root, err := os.OpenRoot(i.Directory)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	if err = checkRoot(root, i); err != nil {
		return Receipt{}, err
	}
	f, err := root.OpenFile(stageName(i), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Receipt{}, err
	}
	_, writeErr := io.WriteString(f, i.Content)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return Receipt{}, writeErr
	}
	if closeErr != nil {
		return Receipt{}, closeErr
	}
	if err = publicationAllowed(ctx, i); err != nil {
		return Receipt{}, err
	}
	if checkpoint == nil {
		return Receipt{}, errors.New("live publication checkpoint required")
	}
	if err = checkpoint(ctx); err != nil {
		return Receipt{}, err
	}
	current, err := DirectoryIdentity(i.Directory)
	if err != nil {
		return Receipt{}, err
	}
	if current != i.DirectoryIdentity {
		return Receipt{}, errors.New("publication directory changed")
	}
	// The authority checkpoint may block while cancellation or expiry occurs.
	// Revalidate immediately before the irreversible link; reconciliation stays
	// read-only and may still observe a publication after authority expires.
	if err = publicationAllowed(ctx, i); err != nil {
		return Receipt{}, err
	}
	if err = root.Link(stageName(i), i.Filename); err != nil {
		return Receipt{}, err
	}
	if runtime.GOOS != "windows" {
		dir, err := root.Open(".")
		if err != nil {
			return Receipt{}, err
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return Receipt{}, err
		}
	}
	return reconcileRoot(ctx, root, i)
}

func Reconcile(ctx context.Context, i FileIntent) (Receipt, error) {
	if err := i.Validate(); err != nil {
		return Receipt{}, err
	}
	root, err := os.OpenRoot(i.Directory)
	if err != nil {
		return Receipt{}, err
	}
	defer root.Close()
	if err = checkRoot(root, i); err != nil {
		return Receipt{}, err
	}
	return reconcileRoot(ctx, root, i)
}
func reconcileRoot(ctx context.Context, root *os.Root, i FileIntent) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	stage, err := root.Lstat(stageName(i))
	if err != nil {
		return Receipt{}, err
	}
	target, err := root.Lstat(i.Filename)
	if err != nil {
		return Receipt{}, err
	}
	if !stage.Mode().IsRegular() || !target.Mode().IsRegular() || !os.SameFile(stage, target) {
		return Receipt{}, errors.New("publication provenance does not match")
	}
	f, err := root.Open(i.Filename)
	if err != nil {
		return Receipt{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Receipt{}, err
	}
	if !os.SameFile(info, target) || info.Size() != int64(len(i.Content)) {
		return Receipt{}, errors.New("publication target changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return Receipt{}, err
	}
	if Hash(b) != i.SHA256 {
		return Receipt{}, errors.New("publication content verification failed")
	}
	return Receipt{ActionID: i.ActionID, Filename: i.Filename, SHA256: i.SHA256, Size: int64(len(b)), Method: "sha256-and-staging-inode"}, nil
}
