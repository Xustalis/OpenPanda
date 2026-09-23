//go:build lite

package panel

import "io/fs"

// Lite build: no embedded frontend. embeddedDist reports absent so
// staticHandler falls through to the lite notice — a constrained node keeps
// the API surface without paying the console's binary and memory cost.
func embeddedDist() (fs.FS, bool) { return nil, false }
