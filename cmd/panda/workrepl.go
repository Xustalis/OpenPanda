// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/taskflow"
)

func (r *repl) cmdWork(arg string)       { r.businessCommand(arg, false) }
func (r *repl) cmdConnection(arg string) { r.businessCommand(arg, true) }
func (r *repl) businessCommand(arg string, connection bool) {
	if r.db == nil {
		r.outln(i18n.T(r.loc, "cli.todo.noStore"))
		return
	}
	env := todoEnv{in: r.commandInput(), out: r.commandOutput(), errW: r.commandError(), loc: r.loc, cfg: r.cfg, configPath: r.configPath, cardPath: r.cardPathNow(), engine: r.engine.Load()}
	path := ""
	if r.cfg != nil {
		path = r.cfg.Storage.DBPath
	}
	var err error
	if connection {
		err = connectionMain(r.commandContext(), taskflow.New(r.db, path), env, splitSlashArgs(arg))
	} else {
		err = workMain(r.commandContext(), r.db, env, splitSlashArgs(arg))
	}
	if err != nil {
		r.outln(pal().Warn(err.Error()))
	}
}
