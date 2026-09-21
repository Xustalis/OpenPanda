//go:build windows

package install

import "testing"

// TestSamePathExpandsPercentVars pins the REG_EXPAND_SZ comparison: a PATH
// entry that still stores %VAR% references must compare equal to the expanded
// literal (os.ExpandEnv only speaks $var/${var}, so this is our own
// expansion), and an undefined variable must stay untouched — never a match
// against a real path.
func TestSamePathExpandsPercentVars(t *testing.T) {
	t.Setenv("OPENPANDA_TEST_INSTALL", `C:\Apps\OpenPanda`)
	if !samePath(`C:\Apps\OpenPanda\bin`, `%OPENPANDA_TEST_INSTALL%\bin`) {
		t.Fatal("samePath must expand %VAR% entries before comparing")
	}
	if samePath(`C:\Apps\OpenPanda\bin`, `%OPENPANDA_UNDEFINED_XYZ%\bin`) {
		t.Fatal("an undefined %VAR% left untouched must not match a real path")
	}
	// Case-insensitivity still applies after expansion.
	if !samePath(`c:\apps\openpanda\bin`, `%OPENPANDA_TEST_INSTALL%\BIN`) {
		t.Fatal("samePath must stay case-insensitive after expansion")
	}
}
