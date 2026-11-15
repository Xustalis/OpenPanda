// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows && !plan9 && !js && !wasip1

package finalaction

import (
	"fmt"
	"os"
	"syscall"
)

func rootIdentity(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("directory identity unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
