// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const sandboxDeleteChild = 0x40
const sandboxFileAllAccess = 0x1f01ff
const sandboxWriteMask = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | sandboxDeleteChild

type windowsSandboxACLPath struct {
	path          string
	handle        windows.Handle
	id            string
	readProtected bool
	temporary     bool
}
type windowsSandboxACLLease struct {
	app, deny   *windows.SID
	paths       []windowsSandboxACLPath
	cancel      windows.Handle
	journal     *windowsSandboxJournal
	nonce       string
	controlRoot string
}

func (l *windowsSandboxACLLease) prepare(req windowsSandboxRequest) error {
	p := req.Policy
	dirs := canonicalPaths(append([]string{p.WorkDir}, p.WritablePaths...))
	readHoles := []string(nil)
	if p.Mode == ModeStrict {
		readHoles = canonicalPaths(p.DenyReadPaths)
	}
	writeHoles := canonicalPaths(append(slices.Clone(p.DenyWritePaths), p.DenyReadPaths...))
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("prepare writable directory %s: %w", dir, err)
		}
		if err := windowsSandboxCheckHardlinks(dir); err != nil {
			return err
		}
	}
	for _, file := range p.WritableFiles {
		if _, err := os.Stat(file); err == nil {
			if err = windowsSandboxCheckHardlinks(file); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	// Validate all writable roots before the first recursive ACL grant: a
	// hardlink in even the last root must not modify an outside file's ACL.
	for _, dir := range dirs {
		if slices.ContainsFunc(readHoles, func(hole string) bool { return sandboxPathWithin(hole, dir) }) {
			continue
		}
		mask := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)
		if !slices.ContainsFunc(writeHoles, func(hole string) bool { return sandboxPathWithin(hole, dir) }) {
			mask |= windows.FILE_GENERIC_WRITE | windows.DELETE
		}
		if err := l.change(dir, l.app, mask, windows.GRANT_ACCESS, true); err != nil {
			return err
		}
	}
	for _, file := range p.WritableFiles {
		if slices.ContainsFunc(readHoles, func(hole string) bool { return sandboxPathWithin(hole, file) }) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		f.Close()
		mask := uint32(windows.FILE_GENERIC_READ)
		if !slices.ContainsFunc(writeHoles, func(hole string) bool { return sandboxPathWithin(hole, file) }) {
			mask |= windows.FILE_GENERIC_WRITE | windows.DELETE
		}
		if err := l.change(file, l.app, mask, windows.GRANT_ACCESS, false); err != nil {
			return err
		}
	}
	requiredReads := []string{filepath.Dir(req.Path)}
	for _, arg := range req.Args[1:] {
		if filepath.IsAbs(arg) {
			if st, err := os.Stat(arg); err == nil {
				if st.IsDir() {
					requiredReads = append(requiredReads, arg)
				} else {
					requiredReads = append(requiredReads, filepath.Dir(arg))
				}
			}
		}
	}
	requiredReads = canonicalPaths(requiredReads)
	for _, dir := range requiredReads {
		if slices.ContainsFunc(readHoles, func(hole string) bool { return sandboxPathWithin(hole, dir) }) {
			continue
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		if slices.ContainsFunc(dirs, func(w string) bool { return sandboxPathWithin(w, dir) }) {
			continue
		}
		if sandboxPublicRead(dir) {
			continue
		}
		if err := l.change(dir, l.app, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE, windows.GRANT_ACCESS, true); err != nil {
			return fmt.Errorf("prepare toolchain read access: %w", err)
		}
	}
	denies := append(slices.Clone(p.DenyWritePaths), p.DenyReadPaths...)
	for _, target := range canonicalPaths(denies) {
		st, err := os.Stat(target)
		if os.IsNotExist(err) {
			accessible := append(slices.Clone(dirs), requiredReads...)
			if slices.ContainsFunc(accessible, func(w string) bool { return sandboxPathWithin(w, target) }) {
				return fmt.Errorf("protected path must exist before sandbox launch: %s", target)
			}
			continue
		}
		if err != nil {
			return err
		}
		mask := uint32(sandboxWriteMask)
		if p.Mode == ModeStrict && slices.ContainsFunc(p.DenyReadPaths, func(d string) bool { return sandboxPathWithin(d, target) }) {
			mask = sandboxFileAllAccess
		}
		if err := l.change(target, l.deny, mask, windows.DENY_ACCESS, st.IsDir()); err != nil {
			return err
		}
		if slices.ContainsFunc(readHoles, func(hole string) bool { return strings.EqualFold(hole, target) }) {
			if err := l.protectRead(target); err != nil {
				return err
			}
		}
		// Removing/renaming an ancestor must not turn an allowed parent into
		// a way around the protected object's own DELETE denial.
		for parent := filepath.Dir(target); parent != filepath.Dir(parent); parent = filepath.Dir(parent) {
			if !slices.ContainsFunc(dirs, func(w string) bool { return sandboxPathWithin(w, parent) }) {
				break
			}
			if err := l.change(parent, l.deny, windows.DELETE|sandboxDeleteChild|windows.WRITE_DAC|windows.WRITE_OWNER, windows.DENY_ACCESS, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func windowsSandboxCheckHardlinks(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return err
		}
		var info windows.ByHandleFileInformation
		err = windows.GetFileInformationByHandle(handle, &info)
		windows.CloseHandle(handle)
		if err != nil {
			return err
		}
		if info.NumberOfLinks > 1 {
			return fmt.Errorf("security: writable root contains a multiply-linked file; refusing to grant its shared inode write access: %s", path)
		}
		return nil
	})
}

func sandboxPathWithin(root, path string) bool {
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(root)), strings.ToLower(filepath.Clean(path)))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func sandboxPublicRead(path string) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return false
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil {
			return false
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if (sid == "S-1-15-2-1" || sid == "S-1-15-2-2") && uint32(ace.Mask)&(windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE) == windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE {
			return true
		}
	}
	return false
}

func (l *windowsSandboxACLLease) change(path string, sid *windows.SID, mask uint32, mode windows.ACCESS_MODE, inherit bool) error {
	if err := l.changeOne(path, sid, mask, mode, inherit); err != nil {
		return err
	}
	if mode == windows.GRANT_ACCESS && windows.EqualSid(sid, l.app) {
		return l.changeOne(path, l.deny, mask, mode, inherit)
	}
	return nil
}

func (l *windowsSandboxACLLease) changeOne(path string, sid *windows.SID, mask uint32, mode windows.ACCESS_MODE, inherit bool) error {
	if l.cancel != 0 {
		if state, err := windows.WaitForSingleObject(l.cancel, 0); err != nil {
			return err
		} else if state == windows.WAIT_OBJECT_0 {
			return context.Canceled
		}
	}
	i := slices.IndexFunc(l.paths, func(p windowsSandboxACLPath) bool { return strings.EqualFold(p.path, path) })
	var handle windows.Handle
	if i >= 0 {
		handle = l.paths[i].handle
	} else {
		var err error
		handle, err = windowsSandboxOpenPolicyPath(path)
		if err != nil {
			return fmt.Errorf("pin policy path %s: %w", path, err)
		}
		id, err := windowsSandboxFileID(handle)
		if err != nil {
			windows.CloseHandle(handle)
			return err
		}
		if alias := slices.IndexFunc(l.paths, func(p windowsSandboxACLPath) bool { return p.id == id }); alias >= 0 {
			windows.CloseHandle(handle)
			handle = l.paths[alias].handle
		} else {
			l.paths = append(l.paths, windowsSandboxACLPath{path: path, handle: handle, id: id})
			if l.journal != nil {
				if err = l.journal.remember(path, handle); err != nil {
					return err
				}
			}
		}
	}
	return sandboxWithHandleACLMutex(handle, func() error {
		sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		old, _, err := sd.DACL()
		if err != nil {
			return err
		}
		flags := uint32(0)
		if inherit {
			flags = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
		}
		acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: windows.ACCESS_MASK(mask), AccessMode: mode, Inheritance: flags, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}}}, old)
		if err != nil {
			return err
		}
		return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	})
}

// A named mutex serializes read/modify/write across launcher processes. Only
// this lease's two unpredictable SIDs are removed; another run's grants and
// concurrent administrator ACL changes survive cleanup.
func (l *windowsSandboxACLLease) close() error {
	var result error
	for i := len(l.paths) - 1; i >= 0; i-- {
		p := l.paths[i]
		err := sandboxWithHandleACLMutex(p.handle, func() error {
			sd, err := windows.GetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				return err
			}
			acl, _, err := sd.DACL()
			if err != nil || acl == nil {
				return err
			}
			data, err := sandboxRemoveACEs(acl, l.app, l.deny)
			if err != nil {
				return err
			}
			filtered := (*windows.ACL)(unsafe.Pointer(&data[0]))
			if p.readProtected {
				return l.restoreRead(p, filtered)
			}
			return windows.SetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, filtered, nil)
		})
		if err == nil && p.temporary {
			err = windowsSandboxEmptyTemporary(p.path)
		}
		windows.CloseHandle(p.handle)
		if err == nil && p.temporary {
			// The directory remained pinned while clearing its children. Only
			// remove the now-empty root after releasing that no-delete handle.
			err = os.Remove(p.path)
			if os.IsNotExist(err) {
				err = nil
			}
			if err == nil {
				_ = os.Remove(filepath.Dir(p.path))               // empty AC projection
				_ = os.Remove(filepath.Dir(filepath.Dir(p.path))) // empty profile
			}
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("restore policy path %s: %w", p.path, err))
		}
	}
	l.paths = nil
	return result
}

func (l *windowsSandboxACLLease) rememberTemporary(path string) error {
	for i := range l.paths {
		p := &l.paths[i]
		if strings.EqualFold(p.path, path) {
			if err := l.journal.append(windowsSandboxJournalRecord{Path: p.path, ID: p.id, Temporary: true}); err != nil {
				return err
			}
			p.temporary = true
			return nil
		}
	}
	return errors.New("security: private temporary directory was not pinned")
}

func windowsSandboxEmptyTemporary(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return err
	}
	for _, entry := range entries {
		if err = root.RemoveAll(entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (l *windowsSandboxACLLease) closeHandles() {
	for _, p := range l.paths {
		windows.CloseHandle(p.handle)
	}
	l.paths = nil
}

func windowsSandboxOpenPolicyPath(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
}

func sandboxRemoveACEs(acl *windows.ACL, sids ...*windows.SID) ([]byte, error) {
	entries := make([][]byte, 0, int(acl.AceCount))
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return nil, err
		}
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if slices.ContainsFunc(sids, func(own *windows.SID) bool { return windows.EqualSid(sid, own) }) {
				continue
			}
		}
		data := slices.Clone(unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize)))
		entries = append(entries, data)
	}
	return sandboxACLBytes(acl, entries)
}

func sandboxACLBytes(acl *windows.ACL, entries [][]byte) ([]byte, error) {
	size := int(unsafe.Sizeof(windows.ACL{}))
	for _, entry := range entries {
		size += len(entry)
	}
	if size > 65535 {
		return nil, errors.New("security: ACL size overflow")
	}
	data := make([]byte, size)
	header := (*windows.ACL)(unsafe.Pointer(&data[0]))
	*header = *acl
	binary.LittleEndian.PutUint16(data[2:4], uint16(size))
	header.AceCount = uint16(len(entries))
	offset := int(unsafe.Sizeof(windows.ACL{}))
	for _, entry := range entries {
		copy(data[offset:], entry)
		offset += len(entry)
	}
	return data, nil
}

func sandboxWithHandleACLMutex(handle windows.Handle, fn func() error) error {
	id, err := windowsSandboxFileID(handle)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sum := sha256.Sum256([]byte(id))
	name, _ := windows.UTF16PtrFromString(fmt.Sprintf(`Local\OpenPanda.Sandbox.ACL.%x`, sum[:16]))
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	defer windows.CloseHandle(mutex)
	status, err := windows.WaitForSingleObject(mutex, 30000)
	if err != nil {
		return err
	}
	if status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED {
		return errors.New("security: timed out waiting for policy ACL lock")
	}
	defer windows.ReleaseMutex(mutex)
	return fn()
}
