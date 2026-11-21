// SPDX-License-Identifier: AGPL-3.0-or-later

import { useLayoutEffect, useRef } from 'preact/hooks'

/** The selection follows the actual row, including wrapped labels and resized rails. */
export function NavigationMarker({ active }: { active: string }) {
  const ref = useRef<HTMLSpanElement>(null)
  useLayoutEffect(() => {
    const marker = ref.current
    const nav = marker?.parentElement
    if (!marker || !nav) return
    const row = nav.querySelector<HTMLElement>('.nav-item.active')
    function place() {
      if (!marker) return
      marker.hidden = !row || !row.offsetHeight
      if (!row || marker.hidden) return
      marker.style.height = row.offsetHeight + 'px'
      marker.style.transform = 'translateY(' + row.offsetTop + 'px)'
    }
    place()
    const observer = new ResizeObserver(place)
    observer.observe(nav)
    if (row) observer.observe(row)
    return () => observer.disconnect()
  }, [active])
  return <span ref={ref} class="nav-marker" aria-hidden="true" hidden />
}
