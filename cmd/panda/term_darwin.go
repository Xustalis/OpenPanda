// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build darwin

package main

import "golang.org/x/sys/unix"

const ioctlReadTermios = unix.TIOCGETA
const ioctlWriteTermios = unix.TIOCSETA
