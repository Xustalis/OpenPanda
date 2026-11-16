// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WRITE_RESTRICTED does not evaluate deny ACEs on reads. A strict read hole
// therefore removes this package's read grant and blocks inheritance at the
// hole's root. Other packages' individual grants remain untouched. A public
// package/capability read grant cannot be safely narrowed per process: refuse
// that policy instead of modifying unrelated applications' permissions.
type windowsSandboxReadState struct {
	Protected bool            `json:"originally_protected"`
	Owners    map[string]bool `json:"owners"`
	Inherited [][]byte        `json:"inherited,omitempty"`
}

func windowsSandboxCheckReadMask(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 && path != root {
			return fmt.Errorf("security: strict read hole contains a reparse point: %s", path)
		}
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		if acl == nil {
			return fmt.Errorf("security: strict read hole has an unrestricted DACL: %s", path)
		}
		for i := uint32(0); i < uint32(acl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err = windows.GetAce(acl, i, &ace); err != nil {
				return err
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
				return fmt.Errorf("security: strict read hole has an unsupported conditional/object ACE: %s", path)
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
				continue
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
			public := sid == "S-1-15-2-1" || sid == "S-1-15-2-2" || strings.HasPrefix(sid, "S-1-15-3-")
			if public && uint32(ace.Mask)&(windows.FILE_READ_DATA|windows.GENERIC_READ|windows.GENERIC_ALL) != 0 {
				return fmt.Errorf("security: strict read hole is publicly readable by AppContainers; refusing to change shared grants: %s", path)
			}
		}
		return nil
	})
}

func (l *windowsSandboxACLLease) protectRead(path string) error {
	if l.journal == nil {
		return errors.New("security: strict read masking requires a recovery journal")
	}
	index := -1
	for i := range l.paths {
		if strings.EqualFold(l.paths[i].path, path) {
			index = i
			break
		}
	}
	if index < 0 {
		handle, err := windowsSandboxOpenPolicyPath(path)
		if err != nil {
			return err
		}
		id, err := windowsSandboxFileID(handle)
		windows.CloseHandle(handle)
		if err != nil {
			return err
		}
		for i := range l.paths {
			if l.paths[i].id == id {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("security: read hole has no pinned policy object: %s", path)
		}
	}
	p := &l.paths[index]
	if p.readProtected {
		return nil
	}
	if err := windowsSandboxCheckReadMask(path); err != nil {
		return err
	}
	return sandboxWithHandleACLMutex(p.handle, func() error {
		sd, err := windows.GetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		control, _, err := sd.Control()
		if err != nil {
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		statePath := l.readStatePath(p.id)
		state, err := windowsSandboxLoadReadState(statePath)
		if os.IsNotExist(err) {
			state = windowsSandboxReadState{Protected: control&windows.SE_DACL_PROTECTED != 0, Owners: map[string]bool{}}
			// Protecting a DACL converts inherited entries into explicit ones.
			// Remember exactly those entries so final unprotection does not
			// leave explicit duplicates alongside newly inherited permissions.
			for i := uint32(0); i < uint32(acl.AceCount); i++ {
				var ace *windows.ACCESS_ALLOWED_ACE
				if err = windows.GetAce(acl, i, &ace); err != nil {
					return err
				}
				if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
					state.Inherited = append(state.Inherited, slices.Clone(unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize))))
				}
			}
		} else if err != nil {
			return err
		}
		// Record cleanup intent before either the refcount or the DACL changes.
		if err = l.journal.append(windowsSandboxJournalRecord{Path: p.path, ID: p.id, ReadProtected: true}); err != nil {
			return err
		}
		p.readProtected = true
		state.Owners[l.nonce] = true
		if err = windowsSandboxSaveReadState(statePath, state, l.nonce); err != nil {
			return err
		}
		data, err := sandboxRemoveACEs(acl, l.app)
		if err != nil {
			return err
		}
		return windows.SetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, (*windows.ACL)(unsafe.Pointer(&data[0])), nil)
	})
}

func (l *windowsSandboxACLLease) readStatePath(id string) string {
	return filepath.Join(l.controlRoot, id+".read-mask")
}

// Called under the object's ACL mutex. Another strict run may still need the
// inheritance barrier: only its final owner may restore the original flag.
func (l *windowsSandboxACLLease) restoreRead(p windowsSandboxACLPath, acl *windows.ACL) error {
	path := l.readStatePath(p.id)
	state, err := windowsSandboxLoadReadState(path)
	if os.IsNotExist(err) {
		return windows.SetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	}
	if err != nil {
		return err
	}
	if !state.Owners[l.nonce] {
		return windows.SetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
	}
	delete(state.Owners, l.nonce)
	flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if len(state.Owners) == 0 && !state.Protected {
		flags = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
		data, err := windowsSandboxUndoInheritedCopies(acl, state.Inherited)
		if err != nil {
			return err
		}
		acl = (*windows.ACL)(unsafe.Pointer(&data[0]))
	}
	if err = windows.SetSecurityInfo(p.handle, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil); err != nil {
		return err
	}
	if len(state.Owners) == 0 {
		return os.Remove(path)
	}
	return windowsSandboxSaveReadState(path, state, l.nonce)
}

func windowsSandboxUndoInheritedCopies(acl *windows.ACL, inherited [][]byte) ([]byte, error) {
	converted := map[string]int{}
	for _, original := range inherited {
		if len(original) < 4 {
			return nil, errors.New("security: invalid inherited recovery ACE")
		}
		data := slices.Clone(original)
		data[1] &^= windows.INHERITED_ACE
		converted[string(data)]++
	}
	var entries [][]byte
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return nil, err
		}
		data := unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(ace.Header.AceSize))
		if converted[string(data)] > 0 {
			converted[string(data)]--
			continue
		}
		entries = append(entries, slices.Clone(data))
	}
	return sandboxACLBytes(acl, entries)
}

func windowsSandboxLoadReadState(path string) (windowsSandboxReadState, error) {
	var state windowsSandboxReadState
	data, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if len(data) > 1<<20 {
		return state, errors.New("security: oversized read-mask recovery state")
	}
	if err = json.Unmarshal(data, &state); err != nil || state.Owners == nil {
		return state, errors.New("security: invalid read-mask recovery state")
	}
	return state, nil
}

func windowsSandboxSaveReadState(path string, state windowsSandboxReadState, nonce string) (err error) {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temporary := path + "." + nonce + ".tmp"
	f, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
