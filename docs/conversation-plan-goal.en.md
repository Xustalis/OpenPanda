# Conversation planning and persistent goals

Web, interactive CLI and TUI share one conversation service. Plan and Goal controls appear after slash-command selection. The Plan marker reflects persistent planning mode; the Goal marker reflects goal editing. A started goal has its own progress bar above the composer.

`/plan` toggles persistent planning. `/plan <description>` enters planning and submits the description. Planning survives subsequent messages, session switches and restarts. It overrides the permission picker with read-only access: mutation tools and implementation dispatch are refused. Exiting planning restores the original permissions.

A complete proposal receives a version. Click **Start implementation**, or enter `/plan confirm <version>` in the terminal. The server accepts the exact current version once. Implementation runs in the current session without creating a goal. If planning paused an existing goal, **Implement and continue goal** resumes that same goal only after successful implementation and task settlement.

`/goal <full objective>` creates and starts a persistent goal. `/goal` opens the editor or displays its state. Web controls and terminal commands use the same service.

| Command | Behavior |
| --- | --- |
| `/goal edit <full objective>` | Replace the objective and revoke the old execution generation |
| `/goal pause` | Prevent the next round; let current work finish |
| `/goal stop` | Pause continuation and interrupt the current round and descendants |
| `/goal resume` | Explicitly resume and reset the consecutive blocker counter |
| `/goal clear` | Remove the current goal, revoke continuation and retain history |
| `/goal --tokens 100000 <objective>` | Create a goal with an explicit token budget |
| `/goal --tokens 200000 resume` | Explicitly raise the budget and resume |
| `/goal -- edit the backlog` | Create the literal objective `edit the backlog` |

`pause`, `stop`, `resume` and `clear` act as controls when entered alone. A phrase such as `/goal stop the bleeding in CI` creates a goal. In the Web console, clearing a saved goal requires confirmation.

Goals have no additional token budget by default. Request-local entry usage and associated task usage are reconciled after each round. Reaching the budget prevents another round; an in-flight request can exceed the limit. Models cannot change the objective, increase budgets or resume paused goals.

An active goal continues after its round and task tree settle. Questions, approvals, user input and other active session work prevent continuation. Closing the browser does not stop the service. Entering Plan pauses continuation; exiting Plan alone does not resume the goal. Explicitly continuing a goal during Plan also exits planning, without implementing an unconfirmed proposal.

A successful task or answer does not complete the goal. The model must submit a full-objective audit through `update_goal`; the controller also checks task and recovery settlement. Editing a completed goal invalidates its old audit and pauses it until the user explicitly resumes. The same concrete blocker must recur in three consecutive rounds before the goal becomes blocked. Explicit resume restarts that counter. Usage limits, budgets, nonretryable errors and repeated empty responses stop unproductive continuation.

SQLite persists state and usage. Leases and execution generations fence concurrent processes. Restart recovery observes surviving tasks before dispatching more work; interrupted implementation continues from stored history after reconciliation. Session deletion, goal replacement and clearing revoke eligibility before cancellation, preventing stale callbacks from dispatching again.

Legacy browser goals require explicit restoration before becoming background work. Forked sessions start in normal mode without inherited goal eligibility. The separate `panda plan run` pipeline remains independent.
