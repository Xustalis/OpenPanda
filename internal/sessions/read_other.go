// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package sessions

import "os"

func readSessionFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
