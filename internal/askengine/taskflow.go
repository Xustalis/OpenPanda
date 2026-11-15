// SPDX-License-Identifier: AGPL-3.0-or-later
package askengine

import "github.com/Xustalis/OpenPanda/internal/taskflow"

// TaskFlow exposes management operations; only the node owner runs its loop.
func (e *Engine) TaskFlow() *taskflow.Service { return e.taskFlow }
