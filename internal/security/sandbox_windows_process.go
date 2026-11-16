// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type sandboxSecurityCapabilities struct {
	SID          *windows.SID
	Capabilities *windows.SIDAndAttributes
	Count        uint32
	Reserved     uint32
}

var errWindowsSandboxLiveJob = errors.New("security: sandbox process tree did not exit")

func windowsSandboxProcess(req windowsSandboxRequest, token windows.Token, sid *windows.SID, cancel windows.Handle) (code uint32, err error) {
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, req.ParentPID)
	if err != nil {
		return 0, fmt.Errorf("watch sandbox owner: %w", err)
	}
	defer windows.CloseHandle(parent)
	attrs, err := windows.NewProcThreadAttributeList(3)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	caps := sandboxSecurityCapabilities{SID: sid}
	var network []windows.SIDAndAttributes
	if req.Policy.AllowNetwork {
		for _, text := range []string{"S-1-15-3-1", "S-1-15-3-2", "S-1-15-3-3"} {
			cap, err := windows.StringToSid(text)
			if err != nil {
				return 0, err
			}
			network = append(network, windows.SIDAndAttributes{Sid: cap, Attributes: windows.SE_GROUP_ENABLED})
		}
		caps.Capabilities = &network[0]
		caps.Count = uint32(len(network))
	}
	const securityCapabilitiesAttribute = 0x20009
	if err = attrs.Update(securityCapabilitiesAttribute, unsafe.Pointer(&caps), unsafe.Sizeof(caps)); err != nil {
		return 0, err
	}
	var handles []windows.Handle
	for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		var h windows.Handle
		if err = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(f.Fd()), windows.CurrentProcess(), &h, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return 0, err
		}
		handles = append(handles, h)
		defer windows.CloseHandle(h)
	}
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return 0, err
	}
	si := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW, ShowWindow: windows.SW_HIDE, StdInput: handles[0], StdOutput: handles[1], StdErr: handles[2]}, ProcThreadAttributeList: attrs.List()}
	jobName, _ := windows.UTF16PtrFromString(windowsSandboxJobName(req.Nonce))
	job, err := windows.CreateJobObject(nil, jobName)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(job)
	defer func() { err = errors.Join(err, windowsSandboxStopJob(job, code)) }()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return 0, err
	}
	// Assignment is part of process creation: even a launcher crash between
	// CreateProcess and ResumeThread must not strand an unowned child.
	const jobListAttribute = 0x2000d
	if err = attrs.Update(jobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return 0, fmt.Errorf("assign sandbox job at creation: %w", err)
	}
	args := make([]string, len(req.Args))
	for i, arg := range req.Args {
		args[i] = syscall.EscapeArg(arg)
	}
	line := req.CmdLine
	if line == "" {
		line = strings.Join(args, " ")
	}
	command, err := windows.UTF16PtrFromString(line)
	if err != nil {
		return 0, err
	}
	application, err := windows.UTF16PtrFromString(req.Path)
	if err != nil {
		return 0, err
	}
	dir, err := windows.UTF16PtrFromString(req.Policy.WorkDir)
	if err != nil {
		return 0, err
	}
	childEnv := windowsSandboxChildEnv(os.Environ())
	if req.TempDir != "" {
		childEnv = slices.DeleteFunc(childEnv, func(entry string) bool {
			key, _, _ := strings.Cut(entry, "=")
			return strings.EqualFold(key, "TEMP") || strings.EqualFold(key, "TMP") || strings.EqualFold(key, "TMPDIR")
		})
		childEnv = append(childEnv, "TEMP="+req.TempDir, "TMP="+req.TempDir, "TMPDIR="+req.TempDir)
	}
	env, err := windowsSandboxEnvironment(childEnv)
	if err != nil {
		return 0, err
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcessAsUser(token, application, command, nil, nil, true, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW|windows.CREATE_SUSPENDED, &env[0], dir, &si.StartupInfo, &pi)
	if err != nil {
		return 0, fmt.Errorf("launch restricted process: %w", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		windows.TerminateJobObject(job, 125)
		return 0, err
	}
	for {
		if state, e := windows.WaitForSingleObject(parent, 0); e != nil {
			return 0, e
		} else if state == windows.WAIT_OBJECT_0 {
			return 130, nil
		}
		if result, e := windows.WaitForSingleObject(cancel, 0); e != nil {
			return 0, e
		} else if result == windows.WAIT_OBJECT_0 {
			return 130, nil
		}
		state, e := windows.WaitForSingleObject(pi.Process, 50)
		if e != nil {
			return 0, e
		}
		if state == windows.WAIT_OBJECT_0 {
			if err = windows.GetExitCodeProcess(pi.Process, &code); err != nil {
				return 0, err
			}
			if code == 0xc0000142 { // STATUS_DLL_INIT_FAILED
				fmt.Fprintln(os.Stderr, ErrSandboxUnavailable.Error()+": Windows DLL initialization failed (0xc0000142); this program may require system resources denied by the selected sandbox policy. Isolation remains enabled; no unconfined retry was performed. See docs/windows-sandbox.md.")
			}
			// The root exiting must not leave inherited-pipe descendants behind.
			return code, nil
		}
	}
}

func windowsSandboxJobName(nonce string) string { return `Local\OpenPanda.Sandbox.Job.` + nonce }

func windowsSandboxTempDir(sid *windows.SID) (string, error) {
	text, err := windows.UTF16PtrFromString(sid.String())
	if err != nil {
		return "", err
	}
	var path *uint16
	hr, _, _ := getProfileFolder.Call(uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(&path)))
	if int32(hr) < 0 {
		return "", fmt.Errorf("locate private sandbox temp: HRESULT %#x", uint32(hr))
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(path))
	folder := windows.UTF16PtrToString(path)
	// Shell folder APIs expand the launcher's inherited environment. The
	// lowbox's profile directory, however, belongs to its Windows identity;
	// redirecting LOCALAPPDATA for an adapter must not redirect its OS profile.
	var token windows.Token
	if err = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		return "", err
	}
	defer token.Close()
	env, err := token.Environ(false)
	if err != nil {
		return "", fmt.Errorf("locate sandbox identity environment: %w", err)
	}
	var actualLocal string
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "LOCALAPPDATA") {
			actualLocal = value
			break
		}
	}
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil || !filepath.IsAbs(actualLocal) {
		return "", fmt.Errorf("locate sandbox profile root: %w", errors.Join(err, errors.New("windows identity must have an absolute LocalAppData directory")))
	}
	if sandboxPathWithin(local, folder) {
		relative, err := filepath.Rel(local, folder)
		if err != nil {
			return "", err
		}
		folder = filepath.Join(actualLocal, relative)
	} else if !sandboxPathWithin(actualLocal, folder) {
		return "", errors.New("security: AppContainer folder is outside its Windows profile")
	}
	return filepath.Join(folder, "Temp"), nil
}

// TerminateJobObject is asynchronous. Restoring deny ACEs before every child
// exits would remove the policy from a still-running descendant.
func windowsSandboxStopJob(job windows.Handle, code uint32) error {
	terminateErr := windows.TerminateJobObject(job, code)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var accounting struct {
			TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
			PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
		}
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return errors.Join(errWindowsSandboxLiveJob, terminateErr, err)
		}
		if accounting.ActiveProcesses == 0 {
			return terminateErr
		}
		if !time.Now().Before(deadline) {
			return errors.Join(errWindowsSandboxLiveJob, terminateErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func windowsSandboxEnvironment(env []string) ([]uint16, error) {
	// Windows requires a sorted, double-NUL-terminated Unicode environment.
	// Env is already restricted by Sandbox.Env and the reexec metadata removed.
	slices.SortFunc(env, func(a, b string) int { return strings.Compare(strings.ToUpper(a), strings.ToUpper(b)) })
	var block []uint16
	for _, entry := range env {
		if strings.ContainsRune(entry, 0) {
			return nil, errors.New("security: NUL in subprocess environment")
		}
		value, e := windows.UTF16FromString(entry)
		if e != nil {
			return nil, e
		}
		block = append(block, value...)
	}
	block = append(block, 0)
	if len(block) == 1 {
		block = append(block, 0)
	}
	return block, nil
}
