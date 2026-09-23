//go:build !windows

package artifact

import "golang.org/x/sys/unix"

// freeBytes is FreeBytes's platform half. Bavail — blocks usable by
// unprivileged callers — is the honest bound: Bfree would count the
// root-reserved blocks this process cannot write to anyway.
func freeBytes(dir string) (int64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
