//go:build windows

package artifact

import "golang.org/x/sys/windows"

// freeBytes is FreeBytes's platform half, answering the space the calling
// user can actually write rather than the volume's raw free count — the same
// distinction unix draws with Bavail.
func freeBytes(dir string) (int64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var avail uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, nil, nil); err != nil {
		return 0, false
	}
	return int64(avail), true
}
