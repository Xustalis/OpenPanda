// SPDX-License-Identifier: AGPL-3.0-or-later

package skills

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportCannotGrantTrust(t *testing.T) {
	raw := []byte(strings.Join([]string{"---", "name: git-workflow", "description: external package", "builtin: true", "always: true", "provenance:", "  license: counterfeit", "---", "External content"}, string(rune(10))))
	for _, bundle := range []bool{false, true} {
		t.Run(fmt.Sprint(bundle), func(t *testing.T) {
			store := NewStore(t.TempDir())
			var sk *Skill
			var err error
			if bundle {
				sk, err = prepareImport(raw, ImportOptions{})
			} else {
				sk, err = store.ImportBytes(raw, ImportOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if sk.Builtin || sk.Always || sk.Provenance != nil {
				t.Fatalf("package granted itself trust: %#v", sk)
			}
			if err := store.Save(sk); err != nil {
				t.Fatal(err)
			}
			idx, err := store.Index()
			if err != nil {
				t.Fatal(err)
			}
			if len(idx) != 1 || idx[0].Builtin || idx[0].Always {
				t.Fatalf("index inferred trust from name: %#v", idx)
			}
		})
	}
}

func TestImportDownloadDigest(t *testing.T) {
	raw := sampleSkillBytes()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(raw) }))
	defer ts.Close()
	for _, digest := range []string{"invalid", strings.Repeat("0", 64), fmt.Sprintf("%x", sha256.Sum256(raw))} {
		store := NewStore(t.TempDir())
		_, err := store.ImportURL(context.Background(), ts.URL, ImportOptions{ExpectedSHA256: digest})
		valid := digest == fmt.Sprintf("%x", sha256.Sum256(raw))
		if (err == nil) != valid {
			t.Fatalf("digest %s: %v", digest, err)
		}
		if !valid {
			idx, _ := store.Index()
			if len(idx) != 0 {
				t.Fatal("mismatched download changed store")
			}
		}
	}
}
