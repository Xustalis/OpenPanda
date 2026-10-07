// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package main

import "golang.org/x/sys/unix"

const ioctlReadTermios = unix.TCGETS
const ioctlWriteTermios = unix.TCSETS
