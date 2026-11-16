// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package install

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestNodeTaskNameMatchesInstaller guards the contract that broke once already:
// the binary's uninstall path and the installer's registration path have to
// agree on the task name.
//
// When they drifted, `panda uninstall` deleted a name nothing had ever created,
// so the logon task survived and kept relaunching a daemon whose binary was
// gone. Nothing caught it: the registration code was in a shell script, the
// deletion code was in Go, and no test looked at both.
//
// The assertion reads the shipped script rather than a copy of its contents, so
// renaming the task in either place turns this red.
func TestNodeTaskNameMatchesInstaller(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "install.ps1")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read %s: %v", script, err)
	}
	src := string(data)

	// The registering call, not a mention: /TN <name> is what schtasks creates.
	if !strings.Contains(src, `/TN "`+nodeTaskName+`"`) {
		t.Fatalf("scripts/install.ps1 does not register /TN %q; "+
			"the uninstall path would delete a task that does not exist", nodeTaskName)
	}
}

// The full uninstall stop includes the node task, the Web task and its wrapper.
// Exercise that exact operation set with local fakes; normal go test must never
// create, end or delete a task on the developer's machine.
func fakeWindowsServices(t *testing.T) (windowsServiceControl, *[][]string) {
	t.Helper()
	setUserHome(t, t.TempDir())
	var operations [][]string
	services := windowsServicesWith(func(name string, args ...string) error {
		operations = append(operations, append([]string{name}, args...))
		return nil
	}, func(path string) error {
		operations = append(operations, []string{"remove", path})
		return nil
	}, webServiceWrapperPath())
	return services, &operations
}

func TestStopServicesCommandsAndWrapperAreIsolated(t *testing.T) {
	services, operations := fakeWindowsServices(t)
	services.stopServices()
	want := [][]string{
		{"schtasks.exe", "/Delete", "/TN", NodeTaskName, "/F"},
		{"schtasks.exe", "/End", "/TN", WebServiceTaskName},
		{"schtasks.exe", "/Delete", "/TN", WebServiceTaskName, "/F"},
		{"remove", filepath.Join(os.Getenv("LOCALAPPDATA"), "openpanda-data", "panda-web.cmd")},
	}
	if !reflect.DeepEqual(*operations, want) {
		t.Fatalf("stop operations = %v, want %v", *operations, want)
	}
}

func TestStopServicesContinuesAfterIndividualFailures(t *testing.T) {
	services, _ := fakeWindowsServices(t)
	var calls int
	services.runCommand = func(name string, args ...string) error {
		calls++
		return errors.New("task absent")
	}
	var removed string
	services.removeFile = func(path string) error {
		removed = path
		return errors.New("wrapper absent")
	}
	services.stopServices()
	if calls != 3 || removed != services.webWrapperPath {
		t.Fatalf("best-effort stop skipped work: commands=%d wrapper=%q", calls, removed)
	}
}

func TestWindowsServiceControlIsLocalToEachCall(t *testing.T) {
	first, _ := fakeWindowsServices(t)
	second := windowsServicesWith(first.runCommand, first.removeFile, first.webWrapperPath)
	first.nodeTask, first.webTask = "fixture-node", "fixture-web"
	first.webWrapperPath = filepath.Join(t.TempDir(), "fixture-web.cmd")
	var targets []string
	first.runCommand = func(name string, args ...string) error {
		targets = append(targets, args[2])
		return nil
	}
	first.removeFile = func(path string) error {
		if path != first.webWrapperPath {
			t.Fatalf("unexpected wrapper path %q", path)
		}
		return nil
	}
	first.stopServices()
	if !reflect.DeepEqual(targets, []string{"fixture-node", "fixture-web", "fixture-web"}) {
		t.Fatal(targets)
	}
	if second.nodeTask != NodeTaskName || second.webTask != WebServiceTaskName || second.webWrapperPath == first.webWrapperPath {
		t.Fatal("local fixture changed another controller's production defaults")
	}
}

func TestStopWebServiceWithoutWrapperPath(t *testing.T) {
	var calls int
	services := windowsServiceControl{
		webTask:    "fixture-web",
		runCommand: func(name string, args ...string) error { calls++; return nil },
		removeFile: func(path string) error {
			t.Fatalf("empty wrapper path triggered removal: %q", path)
			return nil
		},
	}
	services.stopWebService()
	if calls != 2 {
		t.Fatalf("Web stop issued %d commands, want End and Delete", calls)
	}
}

func TestRestartWindowsTaskOnlyRunsAfterSuccessfulEnd(t *testing.T) {
	for _, endFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "absent"}[endFails], func(t *testing.T) {
			var calls [][]string
			services := windowsServiceControl{runCommand: func(name string, args ...string) error {
				calls = append(calls, append([]string{name}, args...))
				if endFails {
					return errors.New("task not running")
				}
				return nil
			}}
			services.restartTask("fixture-task")
			want := [][]string{{"schtasks.exe", "/End", "/TN", "fixture-task"}}
			if !endFails {
				want = append(want, []string{"schtasks.exe", "/Run", "/TN", "fixture-task"})
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("restart operations = %v, want %v", calls, want)
			}
		})
	}
}
