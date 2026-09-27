//go:build !lite

package main

import "github.com/Xustalis/OpenPanda/internal/config"

// printBanner draws the startup screen: the OpenPanda wordmark in figlet
// lettering, then version/workdir info and orientation hints.
func (r *repl) printBanner() {
	th := newTheme(r.loc)
	w := termColumns()
	if w <= 0 {
		w = 80
	}
	activity := r.activityCounts()
	var banner string
	r.readConfig(func(c *config.Config) { banner = renderWelcomeBanner(c, r.loc, w, th, activity) })
	r.outln(banner)
}
