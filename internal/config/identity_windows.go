// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package config

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// platformMachineID reads the Windows install's MachineGuid — written once at
// install and the conventional host fingerprint.
//
// This reads the registry through the API rather than shelling out to
// `reg query`: a subprocess can be blocked by a locked-down shell or a
// security policy that blacklists reg.exe, and the old fallback then quietly
// turned the identity into a hash of the hostname. A probe that fails must be
// the exception, not the everyday path.
func platformMachineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}
