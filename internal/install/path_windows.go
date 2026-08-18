//go:build windows

package install

import (
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

// PATH persistence on Windows lives in HKCU\Environment\Path (REG_EXPAND_SZ
// usually). We read-modify-write the value preserving its type — `setx` is
// deliberately avoided because it truncates PATH at 1024 characters. After
// writing we broadcast WM_SETTINGCHANGE so new Explorer-spawned processes
// pick up the change without a logoff.

// AddToPATH appends dir to the user PATH if absent. Returns a human-readable
// description of what was changed (nil slice = already present).
func AddToPATH(dir string) ([]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return nil, fmt.Errorf("install: open HKCU\\Environment: %w", err)
	}
	defer k.Close()

	val, kind, err := k.GetStringValue("Path")
	if err != nil && err != registry.ErrNotExist {
		return nil, fmt.Errorf("install: read PATH: %w", err)
	}
	for _, e := range splitPath(val) {
		if samePath(e, dir) {
			return nil, nil // already registered
		}
	}
	updated := val
	if updated != "" && !strings.HasSuffix(updated, ";") {
		updated += ";"
	}
	updated += dir
	if err := writePath(k, updated+";", kind); err != nil {
		return nil, err
	}
	broadcastEnvChange()
	return []string{"registry HKCU\\Environment\\Path"}, nil
}

// RemovePATHPersistence drops the install dir from the user PATH and deletes
// any OPENPANDA_* user environment variables the node may have recorded.
// Returns descriptions of everything changed.
func RemovePATHPersistence(dir string) ([]string, error) {
	var changed []string

	if k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.SET_VALUE|registry.QUERY_VALUE); err == nil {
		if val, kind, err := k.GetStringValue("Path"); err == nil {
			var kept []string
			for _, e := range splitPath(val) {
				if e == "" || (dir != "" && samePath(e, dir)) {
					continue
				}
				kept = append(kept, e)
			}
			if strings.Join(kept, ";") != val {
				if err := writePath(k, strings.Join(kept, ";"), kind); err == nil {
					changed = append(changed, "registry HKCU\\Environment\\Path")
				}
			}
		}
		// Sweep OPENPANDA_* user variables (requirement 2.b.ii).
		if names, err := k.ReadValueNames(-1); err == nil {
			for _, n := range names {
				if strings.HasPrefix(strings.ToUpper(n), "OPENPANDA_") {
					if err := k.DeleteValue(n); err == nil {
						changed = append(changed, "env "+n)
					}
				}
			}
		}
		k.Close()
	}
	if len(changed) > 0 {
		broadcastEnvChange()
	}
	return changed, nil
}

// writePath preserves the value type: REG_EXPAND_SZ stays expandable so
// entries like %SystemRoot% keep working.
func writePath(k registry.Key, val string, kind uint32) error {
	var err error
	if kind == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", val)
	} else {
		err = k.SetStringValue("Path", val)
	}
	if err != nil {
		return fmt.Errorf("install: write PATH: %w", err)
	}
	return nil
}

func splitPath(v string) []string {
	return strings.Split(strings.TrimPrefix(strings.TrimSuffix(v, ";"), ";"), ";")
}

// PathPersistedAt reports whether dir is recorded in the user registry PATH.
func PathPersistedAt(dir string) []string {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	val, _, err := k.GetStringValue("Path")
	if err != nil {
		return nil
	}
	for _, e := range splitPath(val) {
		if samePath(e, dir) {
			return []string{"registry HKCU\\Environment\\Path"}
		}
	}
	return nil
}

// broadcastEnvChange tells the system the environment changed, so newly
// launched processes (new terminals included) see the updated PATH without
// a logoff. Failure is harmless — a logoff also applies the change.
func broadcastEnvChange() {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")
	env, _ := syscall.UTF16PtrFromString("Environment")
	const (
		hwndBroadcast = 0xFFFF
		wmSettingChg  = 0x1A
		smtoAbortHang = 0x0002
	)
	proc.Call(hwndBroadcast, wmSettingChg, 0, uintptr(unsafe.Pointer(env)), smtoAbortHang, 5000, 0)
}
