//go:build windows

package hwinfo

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// DiskFreeGB returns free space in GiB on the volume holding dir, for the
// calling user (GetDiskFreeSpaceEx already applies quota accounting).
func DiskFreeGB(dir string) (float64, bool) {
	root := filepath.VolumeName(dir)
	if root == "" {
		root = dir
	}
	if len(root) > 0 && root[len(root)-1] != '\\' {
		root += `\`
	}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, false
	}
	var free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, nil, nil); err != nil {
		return 0, false
	}
	return float64(free) / (1 << 30), true
}
