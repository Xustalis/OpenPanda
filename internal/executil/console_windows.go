// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package executil

import "syscall"

var procGetOEMCP = syscall.NewLazyDLL("kernel32.dll").NewProc("GetOEMCP")

// consoleCodePage reads the system OEM code page (936 on zh-CN, 932 on ja,
// 949 on ko, 950 on zh-TW). cmd and PowerShell 5.1 write redirected output in
// this encoding.
func consoleCodePage() uint32 {
	r, _, _ := procGetOEMCP.Call()
	return uint32(r)
}

// normalizeConsoleText transcodes captured bytes using the system OEM code
// page — see normalizeWithCodePage for the policy.
func normalizeConsoleText(b []byte) string { return normalizeWithCodePage(b, consoleCodePage()) }
