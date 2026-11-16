// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package util

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
