//go:build !windows

package hwinfo

import "golang.org/x/sys/unix"

// DiskFreeGB returns free space in GiB on the filesystem holding dir —
// measured for unprivileged use (Bavail, not Bfree) because that is what a
// task writing results can actually touch.
func DiskFreeGB(dir string) (float64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return float64(st.Bavail) * float64(st.Bsize) / (1 << 30), true
}
