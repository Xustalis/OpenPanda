// SPDX-License-Identifier: AGPL-3.0-or-later

package install

// The console runs as its own user-level service (`panda service install`),
// separate from the daemon's units. The names live here so the installer, the
// updater and `panda service` all speak one spelling: they were duplicated once
// already, and a stop that names a unit nobody registered is invisible until a
// logon launches a deleted binary.
const (
	WebServiceLabel    = "com.openpanda.web"
	WebServiceUnitName = "openpanda-web.service"
	WebServiceTaskName = "OpenPandaWeb"

	// The daemon's names, for callers that need to tell "the daemon owns the
	// node row" from "the console does" without hard-coding a second copy.
	NodeServiceLabel    = "com.openpanda.node"
	NodeServiceUnitName = "openpanda.service"
	NodeTaskName        = "OpenPandaNode"
)
