//go:build !lite

package main

// printBanner draws the startup screen: the OpenPanda wordmark in figlet
// lettering, then node/model/workdir info lines and orientation hints.
func (r *repl) printBanner() {
	th := newTheme(r.loc)
	w := termColumns()
	if w <= 0 {
		w = 80
	}
	r.outln(renderWelcomeBanner(r.cfg, r.loc, w, th, r.activityCounts()))
}
