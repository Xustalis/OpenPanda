// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows && !darwin

package config

// platformMachineID is empty on platforms whose machine id lives in one of the
// files probePhysicalIdentity already reads (/etc/machine-id and the dbus
// fallback). A host with neither falls through to MAC, then hostname.
func platformMachineID() string { return "" }
