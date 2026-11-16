// SPDX-License-Identifier: AGPL-3.0-or-later

import { api, type DevStatus } from '../../api/client'
import { suggestProjectName } from '../../components/dir-utils'

/** Bind a working directory to a project name.
 *
 *  A session cannot carry a directory of its own: the panel derives the work
 *  dir from the thread's project row and falls back to the node's work path
 *  (see the ask handler), so a directory picked for a new chat has to become a
 *  project before it can mean anything. An existing project pointing there is
 *  reused — that is the directory's own group in the rail — and a new
 *  directory gets one named after the folder, so the thread files under it
 *  exactly like every other thread does. */
export async function projectForDir(dir: string): Promise<string> {
  const pl = await api.projects()
  const norm = (p: string) => p.replace(/[\\/]+$/, '').toLowerCase()
  const want = norm(dir)
  const hit = (pl.detail ?? []).find((d) => d.work_dir && norm(d.work_dir) === want)
  if (hit) return hit.name
  const taken = new Set(pl.projects ?? [])
  const base = suggestProjectName(dir) || 'project'
  let name = base
  for (let i = 2; taken.has(name); i++) name = `${base}-${i}`
  const created = await api.createProject({ name, work_dir: dir })
  return created.name
}

/** waitDevReady polls a project's dev server until it can be framed, it fails,
 *  or the declared patience runs out. `alive` lets the caller abandon the wait
 *  when the reader has moved to another thread. */
export async function waitDevReady(
  project: string,
  seconds: number,
  alive: () => boolean,
): Promise<DevStatus | null> {
  const deadline = Date.now() + Math.min(seconds + 5, 120) * 1000
  while (Date.now() < deadline) {
    if (!alive()) return null
    await new Promise((resolve) => setTimeout(resolve, 600))
    if (!alive()) return null
    try {
      const st = await api.devStatus(project)
      if (st.state === 'ready' && st.url) return st
      if (st.state === 'failed' || st.state === 'stopped') return null
    } catch {
      // A transient read failure is not a verdict; keep waiting.
    }
  }
  return null
}
