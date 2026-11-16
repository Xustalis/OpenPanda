// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The write-ahead lease survives a killed launcher. Each path identity is
// synced before changing its ACL; an interrupted last record cannot describe
// an ACL change that has already happened. An exclusive writer handle marks a
// live lease without PID-reuse races. Recovery never follows a replaced path.
type windowsSandboxJournal struct {
	file *os.File
	path string
	root windows.Handle
}

type windowsSandboxJournalRecord struct {
	Nonce         string `json:"nonce,omitempty"`
	App           string `json:"app,omitempty"`
	Deny          string `json:"deny,omitempty"`
	Path          string `json:"path,omitempty"`
	ID            string `json:"id,omitempty"`
	ReadProtected bool   `json:"read_protected,omitempty"`
	Temporary     bool   `json:"temporary,omitempty"`
}

var sandboxOpenJobObject = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW")

func windowsSandboxFileID(handle windows.Handle) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x-%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

func windowsSandboxJournalRoot() (string, windows.Handle, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(cache, "OpenPanda", "sandbox-leases")
	if err = os.MkdirAll(path, 0o700); err != nil {
		return "", 0, err
	}
	handle, err := windowsSandboxOpenPolicyPath(path)
	if err != nil {
		return "", 0, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		windows.CloseHandle(handle)
		return "", 0, err
	}
	// Do not inherit a writable worktree's package grants into the recovery
	// control directory, even when WorkDir contains the user's cache.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err == nil {
		var acl *windows.ACL
		acl, _, err = sd.DACL()
		if err == nil {
			err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
		}
	}
	if err != nil {
		windows.CloseHandle(handle)
		return "", 0, err
	}
	return path, handle, nil
}

func newWindowsSandboxJournal(nonce string, app, deny *windows.SID) (*windowsSandboxJournal, error) {
	root, handle, err := windowsSandboxJournalRoot()
	if err != nil {
		return nil, err
	}
	if err = recoverWindowsSandboxJournals(root); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("recover previous sandbox leases: %w", err)
	}
	path := filepath.Join(root, nonce+".lease")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	fh, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ, nil, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	j := &windowsSandboxJournal{file: os.NewFile(uintptr(fh), path), path: path, root: handle}
	if err = j.append(windowsSandboxJournalRecord{Nonce: nonce, App: app.String(), Deny: deny.String()}); err != nil {
		j.close(false)
		return nil, err
	}
	return j, nil
}

func (j *windowsSandboxJournal) append(record windowsSandboxJournalRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	info, err := j.file.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(data))+1 > 16<<20 {
		return errors.New("security: sandbox recovery journal exceeds 16 MiB")
	}
	if _, err = j.file.Write(append(data, '\n')); err != nil {
		return err
	}
	return j.file.Sync()
}

func (j *windowsSandboxJournal) remember(path string, handle windows.Handle) error {
	id, err := windowsSandboxFileID(handle)
	if err != nil {
		return err
	}
	return j.append(windowsSandboxJournalRecord{Path: path, ID: id})
}

func (j *windowsSandboxJournal) close(clean bool) error {
	err := j.file.Close()
	if clean && err == nil {
		err = os.Remove(j.path)
	}
	return errors.Join(err, windows.CloseHandle(j.root))
}

func recoverWindowsSandboxJournals(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var result error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lease") {
			continue
		}
		nonce := strings.TrimSuffix(entry.Name(), ".lease")
		if data, err := hex.DecodeString(nonce); err != nil || len(data) != 16 {
			continue
		}
		path := filepath.Join(root, entry.Name())
		name, _ := windows.UTF16PtrFromString(path)
		handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			continue // a live owner or a concurrent recovery
		}
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		file := os.NewFile(uintptr(handle), path)
		err = recoverWindowsSandboxJournal(file, nonce)
		err = errors.Join(err, file.Close())
		if err == nil {
			err = os.Remove(path)
		}
		result = errors.Join(result, err)
	}
	return result
}

func recoverWindowsSandboxJournal(file *os.File, nonce string) error {
	info, err := file.Stat()
	if err != nil || info.Size() > 16<<20 {
		return errors.Join(errors.New("invalid sandbox recovery size"), err)
	}
	reader := bufio.NewReader(file)
	line, err := reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		// No ACL mutation or profile creation precedes a synced header.
		return nil
	}
	if err != nil {
		return err
	}
	var meta windowsSandboxJournalRecord
	if err := json.Unmarshal(line, &meta); err != nil || meta.Nonce != nonce {
		return errors.New("invalid sandbox recovery identity")
	}
	app, err := windows.StringToSid(meta.App)
	if err != nil {
		return err
	}
	deny, err := windows.StringToSid(meta.Deny)
	if err != nil {
		return err
	}
	expectedDeny, err := windowsSandboxDenySID(nonce)
	if err != nil || !windows.EqualSid(deny, expectedDeny) {
		return errors.New("sandbox recovery restricting SID does not match its nonce")
	}
	profileName, _ := windows.UTF16PtrFromString("OpenPanda.Sandbox." + nonce)
	var expectedApp *windows.SID
	hr, _, _ := deriveProfileSID.Call(uintptr(unsafe.Pointer(profileName)), uintptr(unsafe.Pointer(&expectedApp)))
	if int32(hr) < 0 {
		return fmt.Errorf("derive recovery profile: HRESULT %#x", uint32(hr))
	}
	defer windows.FreeSid(expectedApp)
	if !windows.EqualSid(app, expectedApp) {
		return errors.New("sandbox recovery package SID does not match its profile")
	}
	// Stop a surviving named job before removing its enforcement ACEs.
	name, _ := windows.UTF16PtrFromString(windowsSandboxJobName(nonce))
	const queryAndTerminate = 0x4 | 0x8
	job, _, openErr := sandboxOpenJobObject.Call(queryAndTerminate, 0, uintptr(unsafe.Pointer(name)))
	if job != 0 {
		err = windowsSandboxStopJob(windows.Handle(job), 130)
		windows.CloseHandle(windows.Handle(job))
	} else if !errors.Is(openErr, windows.ERROR_FILE_NOT_FOUND) {
		err = openErr
	}
	if err != nil {
		return err
	}
	lease := &windowsSandboxACLLease{app: app, deny: deny, nonce: nonce, controlRoot: filepath.Dir(file.Name())}
	for {
		line, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			break // an incomplete append precedes its ACL mutation
		}
		if readErr != nil {
			lease.closeHandles()
			return readErr
		}
		var row windowsSandboxJournalRecord
		if err = json.Unmarshal(line, &row); err != nil {
			lease.closeHandles()
			return errors.New("invalid sandbox recovery path record")
		}
		if row.Path == "" || !filepath.IsAbs(row.Path) || row.ID == "" {
			lease.closeHandles()
			return errors.New("invalid sandbox recovery path")
		}
		handle, e := windowsSandboxOpenPolicyPath(row.Path)
		if errors.Is(e, windows.ERROR_FILE_NOT_FOUND) || errors.Is(e, windows.ERROR_PATH_NOT_FOUND) {
			continue
		}
		if e != nil {
			lease.closeHandles()
			return e
		}
		id, e := windowsSandboxFileID(handle)
		if e != nil || id != row.ID {
			windows.CloseHandle(handle)
			lease.closeHandles()
			return errors.New("sandbox recovery path was replaced; leaving its ACL unchanged")
		}
		duplicate := false
		for i := range lease.paths {
			if lease.paths[i].id == id {
				lease.paths[i].readProtected = lease.paths[i].readProtected || row.ReadProtected
				lease.paths[i].temporary = lease.paths[i].temporary || row.Temporary
				windows.CloseHandle(handle)
				duplicate = true
				break
			}
		}
		if !duplicate {
			lease.paths = append(lease.paths, windowsSandboxACLPath{path: row.Path, handle: handle, id: id, readProtected: row.ReadProtected, temporary: row.Temporary})
		}
	}
	if err = lease.close(); err != nil {
		return err
	}
	profile, _ := windows.UTF16PtrFromString("OpenPanda.Sandbox." + nonce)
	hr, _, _ = deleteProfile.Call(uintptr(unsafe.Pointer(profile)))
	if int32(hr) < 0 && uint32(hr) != 0x80070002 && uint32(hr) != 0x80070003 {
		return fmt.Errorf("recover AppContainer profile: HRESULT %#x", uint32(hr))
	}
	return nil
}
