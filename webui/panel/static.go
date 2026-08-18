package panel

import (
	"embed"
	"io/fs"
	"net/http"
)

// The built web console (webui/app → npm run build) lands in dist/app and is
// folded into the panel binary, preserving single-binary distribution. The
// committed dist/index.html is a placeholder that survives `make web` (vite
// writes only dist/app), so a fresh clone builds fine and shows a friendly
// "run make web" page instead of a white screen.
//
//go:embed all:dist
var distFS embed.FS

// staticHandler serves the web console: an explicit StaticDir (dev overrides,
// tests) wins; then the embedded build in dist/app; a source-only build falls
// back to the placeholder. The console uses hash routing, so no server-side
// path rewrite is needed — / always serves index.html.
func staticHandler(staticDir string) http.Handler {
	if staticDir != "" {
		return http.FileServer(http.Dir(staticDir))
	}
	// Real build present? Serve it.
	if _, err := fs.Stat(distFS, "dist/app/index.html"); err == nil {
		if sub, err := fs.Sub(distFS, "dist/app"); err == nil {
			return http.FileServer(http.FS(sub))
		}
	}
	// Source checkout without `make web`: the placeholder explains what to do.
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}
