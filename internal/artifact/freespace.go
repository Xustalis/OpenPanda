package artifact

import "path/filepath"

// FreeBytes reports how many bytes dir's filesystem can still accept, or
// false when the platform cannot answer. The directory itself may not exist
// yet — staging roots are created lazily — so the walk climbs to the nearest
// existing ancestor; space is a property of the volume, not the path.
//
// With no configured size cap this is the bound that replaces it: an archive
// is allowed to be as large as the disk that has to hold it, and no larger.
// A peer advertising more than the filesystem has free is refused before the
// first byte lands, which is a cleaner failure than writing until ENOSPC.
func FreeBytes(dir string) (int64, bool) {
	for {
		if n, ok := freeBytes(dir); ok {
			return n, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return 0, false
		}
		dir = parent
	}
}
