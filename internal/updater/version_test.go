package updater

import "testing"

func TestAssetNameFor(t *testing.T) {
	tests := []struct {
		os, arch, want string
	}{
		{"darwin", "amd64", "panda-1.2.3-darwin-amd64.tar.gz"},
		{"darwin", "arm64", "panda-1.2.3-darwin-arm64.tar.gz"},
		{"linux", "amd64", "panda-1.2.3-linux-amd64.tar.gz"},
		{"linux", "arm64", "panda-1.2.3-linux-arm64.tar.gz"},
		{"windows", "amd64", "panda-1.2.3-windows-amd64.zip"},
		{"windows", "arm64", "panda-1.2.3-windows-arm64.zip"},
	}
	for _, tt := range tests {
		if got := assetNameFor("1.2.3", tt.os, tt.arch); got != tt.want {
			t.Errorf("assetNameFor(%q, %q) = %q, want %q", tt.os, tt.arch, got, tt.want)
		}
	}
}

func TestCompareVersion(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"older", "v0.0.2", "0.0.3", -1},
		{"equal", "0.0.3", "v0.0.3", 0},
		{"newer", "0.0.10", "0.0.3", 1},
		{"missing patch", "1.2", "1.2.0", 0},
		{"suffix", "1.2-rc1", "1.2", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareVersion(tt.a, tt.b); got != tt.want {
				t.Fatalf("CompareVersion(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestChecksumFor(t *testing.T) {
	data := "abc  panda-1.2.3-linux-amd64.tar.gz\n" +
		"def *panda-1.2.3-windows-amd64.zip\n"
	if got := checksumFor(data, "panda-1.2.3-linux-amd64.tar.gz"); got != "abc" {
		t.Fatalf("checksumFor returned %q", got)
	}
	if got := checksumFor(data, "missing.zip"); got != "" {
		t.Fatalf("checksumFor missing entry = %q", got)
	}
}
