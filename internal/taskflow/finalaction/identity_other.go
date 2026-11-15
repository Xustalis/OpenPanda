// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build plan9 || js || wasip1

package finalaction

import (
	"errors"
	"os"
)

func rootIdentity(*os.Root) (string, error) {
	return "", errors.New("file publication identity is unsupported on this platform")
}
