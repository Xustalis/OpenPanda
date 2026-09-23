//go:build !lite

package panel

import (
	"embed"
	"io/fs"
)

// The built web console (webui/app → npm run build) lands in dist/app and is
// folded into the panel binary, preserving single-binary distribution. The
// committed dist/index.html is a placeholder that survives `make web` (vite
// writes only dist/app), so a fresh clone builds fine and shows a friendly
// "run make web" page instead of a white screen.
//
// Lite builds (-tags lite) compile static_lite.go instead and carry no
// embedded assets at all — the resident-size win is the point of that build.
//
//go:embed all:dist
var distFS embed.FS

// embeddedDist returns the embedded console tree (always present in a full
// build; the placeholder dist/index.html is committed).
func embeddedDist() (fs.FS, bool) { return distFS, true }
