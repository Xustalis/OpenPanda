// The OpenPanda brand mark: a minimal geometric panda head. Pure geometry
// (circles/ellipses), two-tone via currentColor so it adapts to any theme,
// and small enough to inline everywhere (nav, favicon, splash).

export function PandaMark({ size = 28 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      fill="none"
      aria-hidden="true"
      class="panda-mark"
    >
      {/* ears */}
      <circle cx="13" cy="17" r="9" fill="currentColor" />
      <circle cx="51" cy="17" r="9" fill="currentColor" />
      {/* head */}
      <circle cx="32" cy="35" r="23" fill="var(--panda-head, #f5f2ec)" stroke="currentColor" stroke-width="3" />
      {/* eye patches */}
      <ellipse cx="22.5" cy="30" rx="6" ry="8.5" fill="currentColor" transform="rotate(-16 22.5 30)" />
      <ellipse cx="41.5" cy="30" rx="6" ry="8.5" fill="currentColor" transform="rotate(16 41.5 30)" />
      {/* eyes */}
      <circle cx="24" cy="31" r="1.7" fill="var(--panda-head, #f5f2ec)" />
      <circle cx="40" cy="31" r="1.7" fill="var(--panda-head, #f5f2ec)" />
      {/* nose */}
      <ellipse cx="32" cy="41" rx="3.6" ry="2.6" fill="currentColor" />
    </svg>
  )
}

export function PandaWordmark({ size = 28 }: { size?: number }) {
  return (
    <span class="wordmark">
      <PandaMark size={size} />
      <span class="wordmark-text">
        Open<b>Panda</b>
      </span>
    </span>
  )
}
