// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Xustalis/OpenPanda/internal/ledger"
)

// queueFixtureAbility runs this test binary instead of depending on POSIX
// executables or shell builtins that may not exist on a Windows worker.
func queueFixtureAbility(t *testing.T, id, action string, args ...string) ledger.NativeAbility {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"-test.run=^TestQueueNativeCommand$", "--", "panda-queue-fixture", action}
	return ledger.NativeAbility{ID: id, Command: binary, Args: append(argv, args...), Tier: 1}
}

func TestQueueNativeCommand(t *testing.T) {
	i := slices.Index(os.Args, "--")
	if i < 0 || len(os.Args) <= i+2 || os.Args[i+1] != "panda-queue-fixture" {
		return
	}
	args := os.Args[i+3:]
	switch os.Args[i+2] {
	case "echo":
		fmt.Println(strings.Join(args, " "))
	case "sleep":
		if len(args) != 1 {
			t.Fatal("sleep fixture requires one duration")
		}
		delay, err := time.ParseDuration(args[0])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(delay)
	case "hold-once":
		if len(args) != 2 {
			t.Fatal("hold fixture requires ready/release paths")
		}
		ready, err := os.OpenFile(args[0], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if os.IsExist(err) {
			break // only the first invocation holds the device
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = ready.Close(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err = os.Stat(args[1]); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				t.Fatal("fixture was not released")
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "write":
		if len(args) == 0 || len(args)%2 != 0 {
			t.Fatal("write fixture requires path/content pairs")
		}
		for i := 0; i < len(args); i += 2 {
			if err := os.WriteFile(args[i], []byte(args[i+1]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unknown queue fixture command %q", os.Args[i+2])
	}
	os.Exit(0)
}
