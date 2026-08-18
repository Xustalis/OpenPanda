// The OpenPanda brand mark: a round, cheerful panda face. Pure geometry,
// two-tone via currentColor so it adapts to any theme, plus soft blush cheeks
// in the accent color and a happy open smile — small enough to inline
// everywhere (nav, favicon, splash, chat avatar).

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
      <circle cx="13.5" cy="16.5" r="9.5" fill="currentColor" />
      <circle cx="50.5" cy="16.5" r="9.5" fill="currentColor" />
      {/* head */}
      <circle cx="32" cy="36" r="23" fill="var(--panda-head, #f5f2ec)" stroke="currentColor" stroke-width="3" />
      {/* eye patches — big, tilted inward for a friendly baby look */}
      <ellipse cx="22.5" cy="31.5" rx="6.4" ry="9" fill="currentColor" transform="rotate(-18 22.5 31.5)" />
      <ellipse cx="41.5" cy="31.5" rx="6.4" ry="9" fill="currentColor" transform="rotate(18 41.5 31.5)" />
      {/* eyes — glossy, with a sparkle */}
      <circle cx="23.6" cy="32.6" r="2.5" fill="var(--panda-head, #f5f2ec)" />
      <circle cx="40.4" cy="32.6" r="2.5" fill="var(--panda-head, #f5f2ec)" />
      <circle cx="24.5" cy="31.7" r="0.9" fill="currentColor" opacity="0.9" />
      <circle cx="41.3" cy="31.7" r="0.9" fill="currentColor" opacity="0.9" />
      {/* blush */}
      <ellipse cx="15.5" cy="40.5" rx="3.4" ry="1.9" fill="var(--panda-blush, #f2a7b3)" opacity="0.7" />
      <ellipse cx="48.5" cy="40.5" rx="3.4" ry="1.9" fill="var(--panda-blush, #f2a7b3)" opacity="0.7" />
      {/* nose + happy smile */}
      <ellipse cx="32" cy="41" rx="3.2" ry="2.3" fill="currentColor" />
      <path d="M27 45.2 Q32 49.6 37 45.2" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" />
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
