// SPDX-License-Identifier: AGPL-3.0-or-later

import { api, type AgentInfo } from '../api/client.ts'

export type AgentRosterLoader = (refresh?: boolean) => Promise<AgentInfo[]>

/** Share the expensive roster request across the composer and settings view.
 *  Concurrent callers join the same promise; a failure clears only its own
 *  generation so the next open can retry without clobbering a newer refresh. */
export function createAgentRosterLoader(fetchAgents: AgentRosterLoader): AgentRosterLoader {
  let roster: Promise<AgentInfo[]> | null = null
  let generation = 0

  return (refresh = false) => {
    if (refresh) {
      roster = null
      generation++
    }
    if (!roster) {
      const current = ++generation
      roster = fetchAgents(refresh).catch((err: unknown) => {
        if (generation === current) roster = null
        throw err
      })
    }
    return roster
  }
}

export const loadAgentRoster = createAgentRosterLoader(api.agents)
