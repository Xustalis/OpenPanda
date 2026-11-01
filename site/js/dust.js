// SPDX-License-Identifier: AGPL-3.0-or-later

// Drifting star dust for the two dark sections.
//
// These are 2D canvases, not WebGL: the effect is a hundred slow particles and
// the 3D hero already owns the GPU. Each canvas draws a field sized to its
// section, drifts it at a fraction of a pixel per frame, and offsets the whole
// field by scroll progress so the section feels deeper than its background
// photograph. Nothing runs while the section is off-screen or the tab is hidden,
// and with prefers-reduced-motion the field is painted exactly once.

const calm = matchMedia('(prefers-reduced-motion: reduce)').matches

export function initDust() {
  const canvases = [...document.querySelectorAll('canvas.dust')]
  if (!canvases.length) return

  const fields = canvases.map(setupField).filter(Boolean)
  if (!fields.length) return

  const loop = () => {
    requestAnimationFrame(loop)
    for (const f of fields) f.frame()
  }
  loop()
}

function setupField(canvas) {
  const ctx = canvas.getContext('2d')
  const host = canvas.parentElement
  if (!ctx || !host) return null

  const dpr = Math.min(devicePixelRatio || 1, 1.6)
  let w = 0
  let h = 0
  let stars = []
  let offset = 0
  let visible = false

  const build = () => {
    const rect = host.getBoundingClientRect()
    w = Math.max(1, Math.round(rect.width))
    h = Math.max(1, Math.round(rect.height))
    canvas.width = Math.round(w * dpr)
    canvas.height = Math.round(h * dpr)
    canvas.style.width = w + 'px'
    canvas.style.height = h + 'px'
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)

    // One star per ~11 000 px², capped: a 1440×900 band lands near 110.
    const count = Math.min(220, Math.round((w * h) / 6800))
    stars = Array.from({ length: count }, () => ({
      x: Math.random() * w,
      y: Math.random() * h,
      r: 0.5 + Math.random() * 1.1,
      a: 0.16 + Math.random() * 0.5,
      vx: (Math.random() - 0.5) * 0.012,
      vy: (Math.random() - 0.5) * 0.010 - 0.004,
      tw: Math.random() * Math.PI * 2,
      tws: 0.15 + Math.random() * 0.5,
    }))
    paint(0)
  }

  const paint = (t) => {
    ctx.clearRect(0, 0, w, h)
    for (const s of stars) {
      const twinkle = calm ? 1 : 0.78 + 0.22 * Math.sin(t * s.tws + s.tw)
      ctx.globalAlpha = s.a * twinkle
      ctx.fillStyle = '#dceaff'
      ctx.beginPath()
      ctx.arc(s.x, s.y + offset, s.r, 0, Math.PI * 2)
      ctx.fill()
      if (s.r > 1.2) {
        // The brightest few get a halo; without one they read as dead pixels.
        const g = ctx.createRadialGradient(s.x, s.y + offset, 0, s.x, s.y + offset, s.r * 7)
        g.addColorStop(0, 'rgba(190, 219, 255, .30)')
        g.addColorStop(1, 'rgba(190, 219, 255, 0)')
        ctx.globalAlpha = s.a * 0.55 * twinkle
        ctx.fillStyle = g
        ctx.beginPath()
        ctx.arc(s.x, s.y + offset, s.r * 7, 0, Math.PI * 2)
        ctx.fill()
      }
    }
    ctx.globalAlpha = 1
  }

  let frameCount = 0
  let last = performance.now()
  let t = 0

  const frame = () => {
    if (!visible || document.hidden) return
    // Reduced motion: the field was painted once by build()/the observer, and
    // it stays exactly that — no drift, no repaint, no frame work.
    if (calm) return
    const now = performance.now()
    const dt = Math.min(48, now - last)
    last = now
    t += dt / 1000

    // Sub-pixel drift: at 60 fps a star crosses the section in ~90 s.
    for (const s of stars) {
      s.x += s.vx * (dt / 16.7)
      s.y += s.vy * (dt / 16.7)
      if (s.x < 0) s.x += w
      if (s.x > w) s.x -= w
      if (s.y < 0) s.y += h
      if (s.y > h) s.y -= h
    }

    // Scroll parallax, 26 px across a full pass of the section.
    const r = host.getBoundingClientRect()
    const p = (innerHeight - r.top) / (innerHeight + r.height)
    offset = (p - 0.5) * -26

    // Repaint every other frame — a 0.01 px/s drift does not need 60 fps.
    if ((frameCount++ & 1) === 0) paint(t)
  }

  build()
  new IntersectionObserver(([e]) => {
    visible = e.isIntersecting
    if (visible && calm) paint(0)
  }, { rootMargin: '10% 0px' }).observe(host)

  let resizeTimer = 0
  addEventListener('resize', () => {
    clearTimeout(resizeTimer)
    resizeTimer = setTimeout(build, 160)
  })

  return { frame }
}
