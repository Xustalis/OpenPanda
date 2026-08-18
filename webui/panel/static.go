package panel

import (
	"embed"
	"io/fs"
	"net/http"
)

// The built web console (webui/app → npm run build) lands here and is folded
// into the panel binary, preserving single-binary distribution. Only a
// placeholder index.html is committed; `make web` materializes the real app.
//
//go:embed all:dist
var distFS embed.FS

// staticHandler serves the web console: an explicit StaticDir (dev overrides,
// tests) wins; otherwise the embedded build. The console uses hash routing,
// so no server-side path rewrite is needed — / always serves index.html.
func staticHandler(staticDir string) http.Handler {
	if staticDir != "" {
		return http.FileServer(http.Dir(staticDir))
	}
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}
