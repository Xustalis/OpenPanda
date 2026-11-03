export function Logo({ className = "" }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 48 48"
      fill="none"
      aria-hidden="true"
      className={className}
    >
      <ellipse
        cx="24"
        cy="24"
        rx="20"
        ry="9.5"
        transform="rotate(-30 24 24)"
        stroke="currentColor"
        strokeOpacity="0.45"
        strokeWidth="1.2"
      />
      <path
        d="M24 4.5l2.35 16.15L42.5 24l-16.15 3.35L24 43.5l-2.35-16.15L5.5 24l16.15-3.35L24 4.5Z"
        fill="currentColor"
      />
      <circle cx="39.4" cy="12.2" r="2.1" fill="currentColor" fillOpacity="0.85" />
      <circle cx="9.6" cy="37.8" r="1.4" fill="currentColor" fillOpacity="0.6" />
    </svg>
  );
}

