// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package security

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsSandboxEnv = "OPENPANDA_INTERNAL_SANDBOX_"
const windowsSandboxLaunchArgument = "--openpanda-internal-sandbox-launch"

var (
	sandboxUserenv        = windows.NewLazySystemDLL("userenv.dll")
	sandboxAdvapi         = windows.NewLazySystemDLL("advapi32.dll")
	createProfile         = sandboxUserenv.NewProc("CreateAppContainerProfile")
	deriveProfileSID      = sandboxUserenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	getProfileFolder      = sandboxUserenv.NewProc("GetAppContainerFolderPath")
	deleteProfile         = sandboxUserenv.NewProc("DeleteAppContainerProfile")
	createRestrictedToken = sandboxAdvapi.NewProc("CreateRestrictedToken")
)

type windowsSandboxRequest struct {
	Path      string
	Args      []string
	Policy    Policy
	Nonce     string
	ParentPID uint32
	TempDir   string
	CmdLine   string
}

// The write-restricted token admits writes only through a fresh ordinary SID;
// public AppContainer package grants must never open another writable path.
// AppContainer adds the network and read boundary.
func platformBackend() string {
	for _, p := range []*windows.LazyProc{createProfile, deriveProfileSID, getProfileFolder, deleteProfile, createRestrictedToken} {
		if p.Find() != nil {
			return ""
		}
	}
	return "appcontainer"
}

func wrapSubprocess(cmd *exec.Cmd, p Policy) error {
	if p.Mode == ModeOff || p.Mode == "" {
		return nil
	}
	if platformBackend() == "" {
		return backendMissing(p.Mode, "AppContainer APIs are unavailable")
	}
	if err := windowsSandboxCheckLauncherToken(); err != nil {
		return fmt.Errorf("%w: %v", ErrSandboxUnavailable, err)
	}
	if cmd.Err != nil {
		return cmd.Err
	}
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%w: locate sandbox launcher: %v", ErrSandboxUnavailable, err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	p.WorkDir, err = filepath.Abs(p.resolveWorkDir())
	if err != nil {
		return err
	}
	target := cmd.Path
	if !filepath.IsAbs(target) {
		if filepath.VolumeName(target) != "" {
			return errors.New("security: Windows sandbox requires an absolute or working-directory-relative executable")
		}
		target = filepath.Join(p.WorkDir, target)
	}
	p.WritablePaths = canonicalPaths(p.WritablePaths)
	p.WritableFiles = canonicalPaths(p.WritableFiles)
	p.DenyWritePaths = canonicalPaths(p.DenyWritePaths)
	p.DenyReadPaths = canonicalPaths(p.DenyReadPaths)
	args := cmd.Args
	if len(args) == 0 {
		args = []string{target}
	}
	req := windowsSandboxRequest{Path: target, Args: args, Policy: p, Nonce: hex.EncodeToString(nonce[:]), ParentPID: uint32(os.Getpid())}
	if cmd.SysProcAttr != nil {
		req.CmdLine = cmd.SysProcAttr.CmdLine
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("security: Windows sandbox launch request exceeds 1 MiB")
	}
	encoded := base64.RawStdEncoding.EncodeToString(data)
	cmd.Env = windowsSandboxChildEnv(cmd.Env)
	const chunk = 8192
	n := (len(encoded) + chunk - 1) / chunk
	cmd.Env = append(cmd.Env, windowsSandboxEnv+"COUNT="+strconv.Itoa(n))
	for i := 0; i < n; i++ {
		end := (i + 1) * chunk
		if end > len(encoded) {
			end = len(encoded)
		}
		cmd.Env = append(cmd.Env, windowsSandboxEnv+strconv.Itoa(i)+"="+encoded[i*chunk:end])
	}
	cmd.Path = binary
	cmd.Args = []string{binary, windowsSandboxLaunchArgument}
	cmd.Dir = p.WorkDir
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CmdLine = ""
	previousCancel := cmd.Cancel
	if previousCancel != nil {
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
			if err != nil {
				return previousCancel()
			}
			defer windows.CloseHandle(process)
			name, _ := windows.UTF16PtrFromString(windowsSandboxEvent(req.Nonce))
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if state, _ := windows.WaitForSingleObject(process, 0); state == windows.WAIT_OBJECT_0 {
					return nil
				}
				event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name)
				if err == nil {
					err = windows.SetEvent(event)
					windows.CloseHandle(event)
					if err != nil {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			return previousCancel()
		}
	}
	return nil
}

func windowsSandboxEvent(nonce string) string { return `Local\OpenPanda.Sandbox.Cancel.` + nonce }

func windowsSandboxChildEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, value := range env {
		if !strings.HasPrefix(strings.ToUpper(value), windowsSandboxEnv) {
			result = append(result, value)
		}
	}
	return result
}

func init() {
	if len(os.Args) != 2 || os.Args[1] != windowsSandboxLaunchArgument {
		return
	}
	count := os.Getenv(windowsSandboxEnv + "COUNT")
	if count == "" {
		fmt.Fprintln(os.Stderr, "security: sandbox launch envelope is missing")
		os.Exit(125)
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 1 || n > 172 {
		fmt.Fprintln(os.Stderr, "security: invalid sandbox launch envelope")
		os.Exit(125)
	}
	var encoded strings.Builder
	for i := 0; i < n; i++ {
		encoded.WriteString(os.Getenv(windowsSandboxEnv + strconv.Itoa(i)))
	}
	data, err := base64.RawStdEncoding.DecodeString(encoded.String())
	if err != nil || len(data) > 1<<20 {
		fmt.Fprintln(os.Stderr, "security: invalid sandbox launch envelope")
		os.Exit(125)
	}
	var req windowsSandboxRequest
	if err = json.Unmarshal(data, &req); err != nil || len(req.Nonce) != 32 || req.ParentPID == 0 || len(req.Args) == 0 || !filepath.IsAbs(req.Path) || (req.Policy.Mode != ModeStandard && req.Policy.Mode != ModeStrict) {
		fmt.Fprintln(os.Stderr, "security: invalid sandbox launch request")
		os.Exit(125)
	}
	code, err := runWindowsSandbox(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "security: Windows sandbox:", Redact(err.Error()))
		os.Exit(125)
	}
	os.Exit(int(code))
}

func runWindowsSandbox(req windowsSandboxRequest) (code uint32, err error) {
	if err = windowsSandboxCheckLauncherToken(); err != nil {
		return 0, err
	}
	eventName, _ := windows.UTF16PtrFromString(windowsSandboxEvent(req.Nonce))
	event, err := windows.CreateEvent(nil, 1, 0, eventName)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	name := "OpenPanda.Sandbox." + req.Nonce
	profile, _ := windows.UTF16PtrFromString(name)
	denySID, err := windowsSandboxDenySID(req.Nonce)
	if err != nil {
		return 0, err
	}
	var appSID *windows.SID
	hr, _, _ := deriveProfileSID.Call(uintptr(unsafe.Pointer(profile)), uintptr(unsafe.Pointer(&appSID)))
	if int32(hr) < 0 {
		return 0, fmt.Errorf("derive AppContainer profile: HRESULT %#x", uint32(hr))
	}
	defer windows.FreeSid(appSID)
	journal, err := newWindowsSandboxJournal(req.Nonce, appSID, denySID)
	if err != nil {
		return 0, err
	}
	lease := &windowsSandboxACLLease{app: appSID, deny: denySID, cancel: event, journal: journal, nonce: req.Nonce, controlRoot: filepath.Dir(journal.path)}
	createdProfile := false
	defer func() {
		if errors.Is(err, errWindowsSandboxLiveJob) {
			lease.closeHandles()
			err = errors.Join(err, journal.close(false))
			return
		}
		cleanupErr := lease.close()
		if createdProfile {
			hr, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(profile)))
			if int32(hr) < 0 && uint32(hr) != 0x80070002 && uint32(hr) != 0x80070003 {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete AppContainer profile: HRESULT %#x", uint32(hr)))
			}
		}
		err = errors.Join(err, cleanupErr, journal.close(cleanupErr == nil))
	}()
	var createdSID *windows.SID
	hr, _, _ = createProfile.Call(uintptr(unsafe.Pointer(profile)), uintptr(unsafe.Pointer(profile)), uintptr(unsafe.Pointer(profile)), 0, 0, uintptr(unsafe.Pointer(&createdSID)))
	if int32(hr) < 0 {
		return 0, fmt.Errorf("create AppContainer profile: HRESULT %#x", uint32(hr))
	}
	createdProfile = true
	windows.FreeSid(createdSID)
	req.TempDir, err = windowsSandboxTempDir(appSID)
	if err != nil {
		return 0, err
	}
	req.Policy.WritablePaths = append(req.Policy.WritablePaths, req.TempDir)
	// GetTempPath in a lowbox also uses the caller's LocalAppData projection.
	// When that differs from the Windows identity's profile, provision only
	// this nonce's projected Temp as well, and recover its cleanup by file ID.
	projectedTemp := ""
	if local := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(local) {
		candidate := filepath.Join(local, "Packages", strings.ToLower(name), "AC", "Temp")
		if !strings.EqualFold(candidate, req.TempDir) {
			projectedTemp = candidate
			req.Policy.WritablePaths = append(req.Policy.WritablePaths, projectedTemp)
		}
	}
	token, err := windowsSandboxRestrictedToken(denySID)
	if err != nil {
		return 0, err
	}
	defer token.Close()
	req.Policy.DenyWritePaths = append(req.Policy.DenyWritePaths, filepath.Dir(journal.path))
	req.Policy.DenyReadPaths = append(req.Policy.DenyReadPaths, filepath.Dir(journal.path))
	if err = lease.prepare(req); err != nil {
		return 0, err
	}
	if projectedTemp != "" {
		if err = lease.rememberTemporary(projectedTemp); err != nil {
			return 0, err
		}
	}
	return windowsSandboxProcess(req, token, appSID, event)
}

func windowsSandboxDenySID(nonce string) (*windows.SID, error) {
	data, err := hex.DecodeString(nonce)
	if err != nil || len(data) != 16 || strings.ToLower(nonce) != nonce {
		return nil, errors.New("invalid sandbox nonce")
	}
	return windows.StringToSid(fmt.Sprintf("S-1-5-21-%d-%d-%d-2000", binary.LittleEndian.Uint32(data[0:4]), binary.LittleEndian.Uint32(data[4:8]), binary.LittleEndian.Uint32(data[8:12])))
}

func windowsSandboxCheckLauncherToken() error {
	token := windows.GetCurrentProcessToken()
	var appContainer, size uint32
	const tokenIsAppContainer = 29 // TOKEN_INFORMATION_CLASS, Windows 8+
	if err := windows.GetTokenInformation(token, tokenIsAppContainer, (*byte)(unsafe.Pointer(&appContainer)), uint32(unsafe.Sizeof(appContainer)), &size); err != nil {
		return err
	}
	if appContainer != 0 {
		return errors.New("refusing to launch a new sandbox from an AppContainer")
	}
	// A second CreateRestrictedToken intersects restricting SID lists; it must
	// never be used as a way to derive a different capability from a worker.
	var needed uint32
	err := windows.GetTokenInformation(token, windows.TokenRestrictedSids, nil, 0, &needed)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return err
	}
	data := make([]byte, needed)
	if err = windows.GetTokenInformation(token, windows.TokenRestrictedSids, &data[0], needed, &needed); err != nil {
		return err
	}
	if (*windows.Tokengroups)(unsafe.Pointer(&data[0])).GroupCount != 0 {
		return errors.New("refusing to launch from an already restricted token")
	}
	return nil
}

func windowsSandboxRestrictedToken(deny *windows.SID) (windows.Token, error) {
	var source windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &source); err != nil {
		return 0, err
	}
	defer source.Close()
	sids := []windows.SIDAndAttributes{{Sid: deny}}
	var restricted windows.Token
	const disablePrivilegesAndRestrictWrites = 0x1 | 0x8
	ok, _, callErr := createRestrictedToken.Call(uintptr(source), disablePrivilegesAndRestrictWrites, 0, 0, 0, 0, uintptr(len(sids)), uintptr(unsafe.Pointer(&sids[0])), uintptr(unsafe.Pointer(&restricted)))
	if ok == 0 {
		return 0, fmt.Errorf("restrict process token: %w", callErr)
	}
	if err := windowsSandboxDefaultDACL(source, restricted, deny); err != nil {
		restricted.Close()
		return 0, err
	}
	return restricted, nil
}

func windowsSandboxDefaultDACL(source, restricted windows.Token, sid *windows.SID) error {
	var n uint32
	if err := windows.GetTokenInformation(source, windows.TokenDefaultDacl, nil, 0, &n); !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return fmt.Errorf("size sandbox default DACL: %w", err)
	}
	data := make([]byte, n)
	if err := windows.GetTokenInformation(source, windows.TokenDefaultDacl, &data[0], n, &n); err != nil {
		return err
	}
	old := *(**windows.ACL)(unsafe.Pointer(&data[0]))
	trustee := windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Trustee: trustee},
	}, old)
	if err != nil {
		return err
	}
	value := struct{ DACL *windows.ACL }{DACL: acl}
	return windows.SetTokenInformation(restricted, windows.TokenDefaultDacl, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)))
}
