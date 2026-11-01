// SPDX-License-Identifier: AGPL-3.0-or-later

// Hero backdrop — a real sky, turning.
//
// The sky is built as a celestial sphere and spun about a tilted pole, the way
// the real thing moves: every star keeps its place relative to every other one,
// and the whole vault turns as a single rigid body. Nothing here pans a picture;
// the photograph is only the diffuse glow behind the points.
//
// Layers, back to front:
//   · Star field   — ~7 000 procedural points on the same sphere. Positions are
//                    drawn uniform-plus-galactic: 55 % isotropic, 45 % gathered
//                    into the plane the photographic band occupies, so the
//                    points and the plate agree about where the Milky Way is.
//                    Sizes follow a steep power law (many faint, few bright),
//                    colours follow a crude B−V ramp (blue-white → amber).
//   · Milky Way    — ESO/S. Brunier all-sky panorama (CC BY 4.0), 6000×3000
//                    equirect. Dimmed to sit *under* the points: it supplies
//                    dust lanes and unresolved starclouds, not the framing.
//   · Meteors      — sparse, additive streaks in world space (they burn in an
//                    atmosphere; they do not rotate with the vault).
//   · Earth        — NASA Blue Marble + Black Marble DNB city lights, sized so
//                    only its limb enters the frame. Night side out.
//   · Airglow      — a fresnel shell on the limb, breathing very slightly.
//
// Motion: the vault turns at 0.00165 rad/s (≈ one revolution per 63 minutes),
// slow enough to read as stillness at a glance, fast enough that the frame is
// visibly different a minute later. The camera answers the pointer and dollies
// forward as the hero is scrolled away.

import * as THREE from 'three'

const SKY_R = 1600
const EARTH_R = 300
// Centre below and behind the frame: only the top of the cap is in shot, so the
// limb reads as a horizon rather than as a floating planet.
const EARTH_POS = new THREE.Vector3(120, -352, -150)
// The pole sits off-vertical; the vault turns around it, not around the screen.
const POLE_TILT = -0.62
const SPIN = 0.00165

// Position of the band inside the sphere's own frame. The plate is an equirect
// map, so its galactic plane is the local equator (y = 0); this rotation is only
// composition — it decides which corner of the screen the band crosses.
const FRAME_ROT = new THREE.Euler(-0.40, 0.63, 0.22)

const isMobile = matchMedia('(max-width: 720px)').matches
const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches

export function initHero(canvas) {
  let renderer
  try {
    renderer = new THREE.WebGLRenderer({
      canvas, antialias: true, alpha: false, powerPreference: 'high-performance',
    })
  } catch {
    // The stylesheet keys off <body>: `body.no-webgl .hero-canvas` hides the
    // canvas and leaves the static star mat showing through.
    document.body.classList.add('no-webgl')
    return null
  }
  const dpr = Math.min(devicePixelRatio || 1, isMobile ? 1.5 : 1.8)
  renderer.setPixelRatio(dpr)
  renderer.setSize(canvas.clientWidth, canvas.clientHeight, false)
  renderer.setClearColor(0x03050a)
  // No tone mapping: the plate is already a finished photograph, and ACES was
  // what flattened its faint half into grey haze. The Earth is lit to sit in
  // the same range instead.
  renderer.toneMapping = THREE.NoToneMapping

  const scene = new THREE.Scene()
  // No fog: there is no air between the camera and the sky.

  const camera = new THREE.PerspectiveCamera(
    55, canvas.clientWidth / canvas.clientHeight, 0.1, SKY_R * 2.4
  )
  camera.position.set(0, 0, 0)

  const loader = new THREE.TextureLoader()

  // ── The vault: pole → spin → frame ──────────────────────────────────────
  // Three nested groups so the spin axis can be tilted: `polar` holds the tilt,
  // `spin` turns about its own (already tilted) Y, `level` cancels the tilt.
  const polar = new THREE.Group()
  polar.rotation.z = POLE_TILT
  scene.add(polar)
  const spin = new THREE.Group()
  polar.add(spin)
  // `level` undoes the tilt for the artwork itself. Without it the pole would
  // also rotate the composition at phase 0, and the band would land somewhere
  // other than where it was framed.
  const level = new THREE.Group()
  level.rotation.z = -POLE_TILT
  spin.add(level)
  const skyFrame = new THREE.Group()
  skyFrame.rotation.copy(FRAME_ROT)
  level.add(skyFrame)

  // ── Milky Way plate ────────────────────────────────────────────────────
  // Mobile takes the 2K cut (~260 KB); desktop a 3K plate (~750 KB). Both are
  // WebP: the plate is a photograph, and at 1440 px the sphere's projection
  // already stretches the 2K map, so the 4K source only bought decode time and
  // VRAM. The JPEG originals stay in textures/ as the source of these copies.
  let onPlateReady = () => {}
  const skyTex = loader.load(
    isMobile ? 'textures/milkyway_2k.webp' : 'textures/milkyway_3k.webp',
    () => onPlateReady(),
    undefined,
    // Plate missing → fall back to the CSS star mat rather than a black box.
    () => document.body.classList.add('no-webgl')
  )
  skyTex.colorSpace = THREE.SRGBColorSpace
  skyTex.anisotropy = renderer.capabilities.getMaxAnisotropy()

  const sky = new THREE.Mesh(
    new THREE.SphereGeometry(SKY_R, 72, 48),
    new THREE.MeshBasicMaterial({
      map: skyTex, side: THREE.BackSide, depthWrite: false, fog: false,
      // Held back so the procedural points read as the sharp foreground; the
      // plate is the glow and the dust, not the detail.
      color: new THREE.Color().setRGB(0.93, 0.94, 0.99),
    })
  )
  sky.renderOrder = 0
  skyFrame.add(sky)

  // ── Star field ─────────────────────────────────────────────────────────
  const starMat = new THREE.ShaderMaterial({
    transparent: true, depthWrite: false, blending: THREE.AdditiveBlending,
    uniforms: {
      uTime: { value: 0 },
      uDpr: { value: dpr },
    },
    vertexShader: /* glsl */ `
      uniform float uTime;
      uniform float uDpr;
      attribute float aSize;
      attribute float aPhase;
      attribute float aLum;
      attribute vec3 aTint;
      varying vec3 vColor;
      void main() {
        vec4 mv = modelViewMatrix * vec4(position, 1.0);
        gl_Position = projectionMatrix * mv;
        // Magnitude → pixel size, with the usual 1/z falloff. The constant is
        // tuned for SKY_R: 1 unit of aSize ≈ 0.21 px at the sphere.
        gl_PointSize = aSize * uDpr * (340.0 / -mv.z);
        // A 6 % luminance ripple. Space has no scintillation; this is the eye's
        // own noise, not a twinkle animation.
        float ripple = 0.94 + 0.06 * sin(uTime * 1.7 + aPhase);
        vColor = aTint * aLum * ripple;
      }`,
    fragmentShader: /* glsl */ `
      varying vec3 vColor;
      void main() {
        vec2 p = gl_PointCoord - 0.5;
        float r2 = dot(p, p) * 4.0;
        if (r2 > 1.0) discard;
        // A tight core plus a wide, faint halo — how a point source lands on a
        // sensor, and what keeps sub-pixel stars from looking like dust.
        float core = exp(-r2 * 7.5);
        float halo = exp(-r2 * 2.4) * 0.22;
        float a = clamp(core + halo, 0.0, 1.0);
        gl_FragColor = vec4(vColor, a);
      }`,
  })

  const starCount = reduced ? 2200 : (isMobile ? 2600 : 6200)
  {
    const pos = new Float32Array(starCount * 3)
    const size = new Float32Array(starCount)
    const phase = new Float32Array(starCount)
    const lum = new Float32Array(starCount)
    const tint = new Float32Array(starCount * 3)

    // Rough B−V ramp, weighted the way a naked-eye sky actually looks: mostly
    // white, a good number of blue-white, a scattering of amber.
    const ramp = [
      [0.78, 0.86, 1.00, 0.20], // B  blue-white
      [0.88, 0.93, 1.00, 0.30], // A  white-blue
      [1.00, 0.99, 0.98, 0.28], // F/G white
      [1.00, 0.94, 0.84, 0.14], // K  cream
      [1.00, 0.84, 0.68, 0.08], // M  amber
    ]
    const pickTint = () => {
      let r = Math.random()
      for (const [cr, cg, cb, w] of ramp) {
        if ((r -= w) <= 0) return [cr, cg, cb]
      }
      return [1, 1, 1]
    }

    const dir = new THREE.Vector3()
    for (let i = 0; i < starCount; i++) {
      // 45 % gathered into the galactic plane (local y ≈ 0), rest isotropic.
      let y
      if (Math.random() < 0.45) {
        y = gauss() * 0.085
      } else {
        y = Math.random() * 2 - 1
      }
      const theta = Math.random() * Math.PI * 2
      const rxy = Math.sqrt(Math.max(0, 1 - y * y))
      dir.set(Math.cos(theta) * rxy, y, Math.sin(theta) * rxy).normalize()

      pos[i * 3] = dir.x * SKY_R
      pos[i * 3 + 1] = dir.y * SKY_R
      pos[i * 3 + 2] = dir.z * SKY_R

      // Steep power law: ~ 80 % of points stay under 2 px.
      const m = Math.pow(Math.random(), 3.4)
      size[i] = 5.0 + 40 * m
      lum[i] = 0.45 + 0.75 * Math.pow(Math.random(), 1.6) + 0.55 * m
      phase[i] = Math.random() * Math.PI * 2
      const [cr, cg, cb] = pickTint()
      tint[i * 3] = cr
      tint[i * 3 + 1] = cg
      tint[i * 3 + 2] = cb
    }

    const geo = new THREE.BufferGeometry()
    geo.setAttribute('position', new THREE.BufferAttribute(pos, 3))
    geo.setAttribute('aSize', new THREE.BufferAttribute(size, 1))
    geo.setAttribute('aPhase', new THREE.BufferAttribute(phase, 1))
    geo.setAttribute('aLum', new THREE.BufferAttribute(lum, 1))
    geo.setAttribute('aTint', new THREE.BufferAttribute(tint, 3))

    const stars = new THREE.Points(geo, starMat)
    stars.frustumCulled = false
    stars.renderOrder = 1
    skyFrame.add(stars)
  }

  // ── Diffraction spikes on the brightest few ────────────────────────────
  // Real astrographs put a cross on anything bright enough to clip. Six of
  // them, placed on the same vault, are what make the field look photographed
  // rather than plotted.
  const spikes = []
  if (!reduced) {
    const spikeTex = makeSpikeTexture()
    const n = isMobile ? 3 : 6
    for (let i = 0; i < n; i++) {
      const s = new THREE.Sprite(new THREE.SpriteMaterial({
        map: spikeTex, transparent: true, blending: THREE.AdditiveBlending,
        depthWrite: false, opacity: 0.55 + Math.random() * 0.3, toneMapped: false,
      }))
      const y = (Math.random() * 2 - 1) * 0.55
      const t = Math.random() * Math.PI * 2
      const rxy = Math.sqrt(1 - y * y)
      s.position.set(
        Math.cos(t) * rxy * SKY_R * 0.995,
        y * SKY_R * 0.995,
        Math.sin(t) * rxy * SKY_R * 0.995
      )
      const sz = 52 + Math.random() * 40
      s.scale.setScalar(sz)
      s.renderOrder = 2
      skyFrame.add(s)
      spikes.push(s)
    }
  }

  // ── Meteors ────────────────────────────────────────────────────────────
  // A short additive line with per-vertex colour (head white → tail black) plus
  // a single glow sprite at the head. Additive blending means "black" is simply
  // "no contribution", which is how the trail fades without an alpha ramp.
  const METEORS = reduced ? 0 : (isMobile ? 2 : 4)
  const TRAIL = 18
  const skyGroup = new THREE.Group()
  scene.add(skyGroup)

  const meteorPool = []
  if (METEORS) {
    const sprite = makeGlowTexture()
    for (let i = 0; i < METEORS; i++) {
      const geo = new THREE.BufferGeometry()
      geo.setAttribute('position', new THREE.BufferAttribute(new Float32Array(TRAIL * 3), 3))
      geo.setAttribute('color', new THREE.BufferAttribute(new Float32Array(TRAIL * 3), 3))
      const line = new THREE.Line(geo, new THREE.LineBasicMaterial({
        vertexColors: true, transparent: true, blending: THREE.AdditiveBlending,
        depthWrite: false, toneMapped: false,
      }))
      line.frustumCulled = false
      line.visible = false

      const head = new THREE.Sprite(new THREE.SpriteMaterial({
        map: sprite, transparent: true, blending: THREE.AdditiveBlending,
        depthWrite: false, opacity: 0, toneMapped: false,
      }))
      head.scale.setScalar(9)
      head.visible = false

      skyGroup.add(line, head)
      meteorPool.push({
        line, head, live: false, t: 0, dur: 1,
        origin: new THREE.Vector3(), dir: new THREE.Vector3(),
        speed: 600, len: 150, headPos: new THREE.Vector3(),
      })
    }
  }

  let nextMeteor = 3.5

  function spawnMeteor(m) {
    // Anywhere in the upper half of the frame, biased toward the open sky on
    // the right so streaks never cross the headline.
    const az = (Math.random() - 0.35) * 0.9
    const el = 0.08 + Math.random() * 0.46
    const dirFromCam = new THREE.Vector3(
      Math.sin(az) * Math.cos(el), Math.sin(el), -Math.cos(az) * Math.cos(el)
    )
    m.origin.copy(dirFromCam).multiplyScalar(900)

    // Tangent to the view direction, falling a little — the geometry of a
    // meteor seen from the ground.
    const up = new THREE.Vector3(0, 1, 0)
    const side = new THREE.Vector3().crossVectors(dirFromCam, up).normalize()
    if (Math.random() < 0.5) side.negate()
    m.dir.copy(side).setY(-0.22 - Math.random() * 0.4).normalize()

    m.speed = 380 + Math.random() * 340
    m.len = 90 + Math.random() * 90
    m.dur = (m.len / m.speed) * (2.2 + Math.random())
    m.t = 0
    m.live = true
    m.line.visible = true
    m.head.visible = true
  }

  function stepMeteors(dt, elapsed) {
    if (!METEORS) return
    if (elapsed > nextMeteor) {
      const idle = meteorPool.find((m) => !m.live)
      if (idle) {
        spawnMeteor(idle)
        nextMeteor = elapsed + 7 + Math.random() * 9
      } else {
        nextMeteor = elapsed + 1.5
      }
    }

    for (const m of meteorPool) {
      if (!m.live) continue
      m.t += dt
      const k = m.t / m.dur
      if (k >= 1) {
        m.live = false
        m.line.visible = false
        m.head.visible = false
        continue
      }

      m.headPos.copy(m.origin).addScaledVector(m.dir, m.speed * m.t)
      const pos = m.line.geometry.attributes.position
      const col = m.line.geometry.attributes.color
      const spacing = m.len / (TRAIL - 1)
      // Fade the whole streak in and out over its life so it never pops.
      const life = Math.min(1, k * 6) * Math.min(1, (1 - k) * 4)
      for (let i = 0; i < TRAIL; i++) {
        const p = m.headPos.clone().addScaledVector(m.dir, -i * spacing)
        pos.setXYZ(i, p.x, p.y, p.z)
        const f = Math.pow(1 - i / (TRAIL - 1), 2.6) * life
        col.setXYZ(i, f * 0.82, f * 0.92, f)
      }
      pos.needsUpdate = true
      col.needsUpdate = true

      m.head.position.copy(m.headPos)
      m.head.material.opacity = 0.85 * life
      m.head.scale.setScalar(7 + 5 * Math.sin(Math.PI * Math.min(k * 2, 1)))
    }
  }

  // ── Earth ──────────────────────────────────────────────────────────────
  const dayTex = loader.load('textures/earth_day_2k.webp')
  const nightTex = loader.load('textures/earth_night_2k.webp')
  const cloudTex = loader.load('textures/clouds_2k.webp')
  for (const t of [dayTex, nightTex]) {
    t.colorSpace = THREE.SRGBColorSpace
    t.anisotropy = renderer.capabilities.getMaxAnisotropy()
  }

  // Planet, clouds and airglow share one group so the pointer parallax can move
  // them together — separate offsets would break the limb alignment.
  const earthGroup = new THREE.Group()
  earthGroup.position.copy(EARTH_POS)
  scene.add(earthGroup)

  const earth = new THREE.Mesh(
    new THREE.SphereGeometry(EARTH_R, 96, 64),
    new THREE.MeshStandardMaterial({
      map: dayTex, roughness: 1, metalness: 0,
      // Real Blue Marble colour — the old version tinted the whole planet
      // grey-green, which is what made it look like a fictional world.
      color: 0xffffff,
      emissiveMap: nightTex, emissive: 0xffd7a8, emissiveIntensity: 1.15,
    })
  )
  earth.rotation.z = -0.16
  earthGroup.add(earth)

  const clouds = new THREE.Mesh(
    new THREE.SphereGeometry(EARTH_R * 1.004, 96, 64),
    new THREE.MeshStandardMaterial({
      map: cloudTex, alphaMap: cloudTex, transparent: true, opacity: 0.6,
      depthWrite: false, roughness: 1, metalness: 0, color: 0xf2f5f7,
    })
  )
  clouds.rotation.z = -0.16
  earthGroup.add(clouds)

  // Airglow: a fresnel rim on a back-side shell, the thin blue edge that
  // orbital photographs show along the limb.
  const atmoMat = new THREE.ShaderMaterial({
    transparent: true, depthWrite: false, side: THREE.BackSide,
    blending: THREE.AdditiveBlending,
    uniforms: {
      uRim: { value: new THREE.Color(0x74b4f0) },
      uTime: { value: 0 },
    },
    vertexShader: /* glsl */ `
      varying vec3 vN;
      void main() {
        vN = normalize(normalMatrix * normal);
        gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0);
      }`,
    fragmentShader: /* glsl */ `
      uniform vec3 uRim;
      uniform float uTime;
      varying vec3 vN;
      void main() {
        float rim = pow(1.0 - abs(dot(vN, vec3(0.0, 0.0, 1.0))), 5.5);
        // A slow breath, the way the airglow layer drifts.
        float breathe = 0.9 + 0.1 * sin(uTime * 0.35);
        gl_FragColor = vec4(uRim, 1.0) * rim * 3.4 * breathe;
      }`,
  })
  const atmo = new THREE.Mesh(new THREE.SphereGeometry(EARTH_R * 1.012, 64, 40), atmoMat)
  earthGroup.add(atmo)

  // Sun behind the Earth: the visible cap falls into night, the DNB cities come
  // through as emitters, and the limb keeps its airglow. (Put the sun back at
  // (-620, 760, 380) for the daylit Blue Marble instead.)
  scene.add(new THREE.AmbientLight(0x1b2634, 1.6))
  const sun = new THREE.DirectionalLight(0xfff4e2, 4.6)
  sun.position.set(300, -820, -360)
  scene.add(sun)

  // ── Pointer parallax ───────────────────────────────────────────────────
  const aim = { x: 0, y: 0 }
  const look = { x: 0, y: 0 }
  const fine = matchMedia('(pointer: fine)').matches

  // Pointer parallax is interaction-triggered motion; reduced-motion users
  // keep the still frame instead.
  if (fine && !reduced) {
    canvas.addEventListener('pointermove', (e) => {
      const r = canvas.getBoundingClientRect()
      aim.x = ((e.clientX - r.left) / r.width - 0.5) * 0.055
      aim.y = -((e.clientY - r.top) / r.height - 0.5) * 0.032
    })
    canvas.addEventListener('pointerleave', () => { aim.x = 0; aim.y = 0 })
  }

  let visible = true
  let needsPaint = true
  new IntersectionObserver(([e]) => {
    visible = e.isIntersecting
    if (visible) needsPaint = true
  }, { threshold: 0 }).observe(canvas)
  // A background tab gets no frames at all — stop paying for them when hidden.
  let awake = !document.hidden
  document.addEventListener('visibilitychange', () => {
    awake = !document.hidden
    if (awake) needsPaint = true
  })

  // ── Scroll: the hero is a descent, not a still ─────────────────────────
  // Scrolling dollies the camera forward and tips it down, so the planet grows
  // and the band slides behind the copy instead of sitting frozen. Like the
  // pointer parallax, this is motion the reduced-motion setting turns off.
  let scrollTarget = 0
  let scroll = 0
  const readScroll = () => {
    const h = canvas.clientHeight || innerHeight || 1
    scrollTarget = Math.min(Math.max(scrollY / h, 0), 1.15)
  }
  if (!reduced) {
    addEventListener('scroll', readScroll, { passive: true })
    readScroll()
  }

  // Portrait frames put the copy straight over the galactic plane; tilt the
  // camera up so the band drops to the bottom of the screen.
  let portraitPitch = camera.aspect < 0.85 ? 0.13 : 0

  let resizeTimer = 0
  addEventListener('resize', () => {
    clearTimeout(resizeTimer)
    resizeTimer = setTimeout(() => {
      const w = canvas.clientWidth, h = canvas.clientHeight
      if (!w || !h) return
      camera.aspect = w / h
      camera.updateProjectionMatrix()
      portraitPitch = camera.aspect < 0.85 ? 0.13 : 0
      renderer.setSize(w, h, false)
      readScroll()
      needsPaint = true
    }, 120)
  })

  // ── Frame loop ─────────────────────────────────────────────────────────
  const clock = new THREE.Clock()
  let announced = false
  let elapsed = 0
  let spinPhase = 0

  function frame() {
    requestAnimationFrame(frame)
    if (!visible || !awake) return
    // Under reduced motion the scene is a still: render only when something
    // actually changed (first paint, resize, tab return), not every vsync.
    if (reduced && !needsPaint) return
    const dt = Math.min(clock.getDelta(), 0.05)
    if (!reduced) elapsed += dt
    const t = reduced ? 0 : elapsed

    // The vault turns as one body. Reduced motion keeps it still.
    if (!reduced) {
      spinPhase += dt * SPIN
      spin.rotation.y = spinPhase
    }
    starMat.uniforms.uTime.value = t
    earth.rotation.y = t * 0.0032
    clouds.rotation.y = t * 0.0046
    atmoMat.uniforms.uTime.value = t
    stepMeteors(dt, elapsed)

    look.x += (aim.x - look.x) * 0.05
    look.y += (aim.y - look.y) * 0.05
    scroll += (scrollTarget - scroll) * 0.06

    // Planets sit nearer than the sky, so they shift further with the pointer.
    earthGroup.position.x = EARTH_POS.x + look.x * 120
    earthGroup.position.y = EARTH_POS.y + look.y * 90

    camera.position.z = -scroll * 46
    camera.position.y = scroll * 12
    camera.rotation.set(
      portraitPitch + look.y + scroll * 0.06, look.x * 1.35, 0, 'YXZ'
    )

    renderer.render(scene, camera)
    needsPaint = false

    // Only reveal the canvas once the plate is actually in the texture — a
    // black frame fading in over the still looks like a bug.
    if (!announced && skyTex.image) {
      announced = true
      document.body.classList.add('scene-ready')
    }
  }

  // Hold the fade-in until the plate is decoded, otherwise the canvas fades in
  // on a black frame and the photograph appears to pop in afterwards.
  let started = false
  const start = () => {
    // A late plate still has to produce one more frame: under reduced motion
    // the loop only paints when needsPaint is set, so a start that arrives
    // after the safety net would otherwise never reveal the canvas.
    needsPaint = true
    if (started) return
    started = true
    frame()
  }
  onPlateReady = start
  // Safety net: a cached/stalled request must not leave the hero black forever.
  setTimeout(start, 3000)

  return { isReady: () => announced }
}

// Box–Muller, trimmed to one value. Used for the thickness of the galactic
// plane, where a gaussian gives soft edges instead of a hard slab.
function gauss() {
  let u = 0, v = 0
  while (u === 0) u = Math.random()
  while (v === 0) v = Math.random()
  return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * v)
}

// A single soft dot, used as the head of every meteor.
function makeGlowTexture() {
  const size = 64
  const c = document.createElement('canvas')
  c.width = c.height = size
  const g = c.getContext('2d')
  const grd = g.createRadialGradient(size / 2, size / 2, 0, size / 2, size / 2, size / 2)
  grd.addColorStop(0, 'rgba(255,255,255,1)')
  grd.addColorStop(0.2, 'rgba(214,238,255,0.7)')
  grd.addColorStop(0.5, 'rgba(150,195,255,0.2)')
  grd.addColorStop(1, 'rgba(120,170,255,0)')
  g.fillStyle = grd
  g.fillRect(0, 0, size, size)
  const tex = new THREE.CanvasTexture(c)
  tex.colorSpace = THREE.SRGBColorSpace
  return tex
}

// The four-point cross a bright star throws on a real lens: two long spikes on
// the axes, a small hot core, and a soft bloom between them.
function makeSpikeTexture() {
  const s = 128
  const c = document.createElement('canvas')
  c.width = c.height = s
  const g = c.getContext('2d')
  g.translate(s / 2, s / 2)

  const bloom = g.createRadialGradient(0, 0, 0, 0, 0, s * 0.34)
  bloom.addColorStop(0, 'rgba(255,255,255,0.95)')
  bloom.addColorStop(0.22, 'rgba(214,235,255,0.42)')
  bloom.addColorStop(1, 'rgba(170,205,255,0)')
  g.fillStyle = bloom
  g.beginPath()
  g.arc(0, 0, s * 0.34, 0, Math.PI * 2)
  g.fill()

  // Fade along the long axis, so each spike dies out before it reaches the
  // sprite's edge instead of ending on a hard line.
  for (const [w, h, a] of [[s * 0.98, 1.6, 0.55], [1.6, s * 0.98, 0.55], [s * 0.55, 1.0, 0.3], [1.0, s * 0.55, 0.3]]) {
    const vertical = h > w
    const lg = vertical
      ? g.createLinearGradient(0, -h / 2, 0, h / 2)
      : g.createLinearGradient(-w / 2, 0, w / 2, 0)
    lg.addColorStop(0, 'rgba(255,255,255,0)')
    lg.addColorStop(0.5, `rgba(255,255,255,${a})`)
    lg.addColorStop(1, 'rgba(255,255,255,0)')
    g.fillStyle = lg
    g.fillRect(-w / 2, -h / 2, w, h)
  }

  const tex = new THREE.CanvasTexture(c)
  tex.colorSpace = THREE.SRGBColorSpace
  return tex
}
