package panel

import (
	"io/fs"
	"net/http"
)

// staticHandler serves the web console: an explicit StaticDir (dev overrides,
// tests) wins; then the embedded build in dist/app; a source-only build falls
// back to the placeholder. The console uses hash routing, so no server-side
// path rewrite is needed — / always serves index.html.
//
// The embedded FS itself lives behind a build split: full builds embed
// dist/, lite builds (go build -tags lite, for constrained nodes) carry no
// frontend at all and serve the lite notice instead — the API still answers.
func staticHandler(staticDir string) http.Handler {
	if staticDir != "" {
		return http.FileServer(http.Dir(staticDir))
	}
	if dist, ok := embeddedDist(); ok {
		// Real build present? Serve it.
		if _, err := fs.Stat(dist, "dist/app/index.html"); err == nil {
			if sub, err := fs.Sub(dist, "dist/app"); err == nil {
				return http.FileServer(http.FS(sub))
			}
		}
		// Source checkout without `make web`: the placeholder explains what
		// to do.
		if sub, err := fs.Sub(dist, "dist"); err == nil {
			return http.FileServer(http.FS(sub))
		}
	}
	return liteNoticeHandler()
}

// liteNoticeHandler serves the single-page explanation used when this binary
// carries no embedded console (lite build, or a missing dist).
func liteNoticeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(liteNoticeHTML))
	})
}

const liteNoticeHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>OpenPanda — lite</title>
<style>body{font-family:system-ui,sans-serif;max-width:34em;margin:4em auto;padding:0 1em;color:#333;line-height:1.6}</style>
</head><body>
<h1>OpenPanda — lite build</h1>
<p>此节点以精简版构建（<code>-tags lite</code>）运行，未内嵌 Web 控制台——
适用于树莓派、嵌入式 Linux 和纯命令行环境，省内存省体积。</p>
<p>This is a lite build: the embedded web console is not included.
The API still answers on this port (<code>/api/*</code>), and the node is
fully operable through <code>panda</code> CLI commands.</p>
<p>需要控制台？Use the full build (<code>make build</code>), or point a full
node's <code>panda web</code> at this node's API.</p>
</body></html>
`
