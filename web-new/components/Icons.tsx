type IconProps = { className?: string; size?: number };

function base({ className = "", size = 18 }: IconProps) {
  return {
    className,
    width: size,
    height: size,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.5,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
  };
}

export function IconOrchestrate(props: IconProps) {
  return (
    <svg {...base(props)}>
      <rect x="9" y="3" width="6" height="5" rx="1.4" />
      <rect x="2.5" y="16" width="6" height="5" rx="1.4" />
      <rect x="15.5" y="16" width="6" height="5" rx="1.4" />
      <path d="M12 8v4M5.5 16v-2h13v2M12 12v2" />
      <circle cx="12" cy="12" r="1" fill="currentColor" stroke="none" />
    </svg>
  );
}

export function IconChip(props: IconProps) {
  return (
    <svg {...base(props)}>
      <rect x="6" y="6" width="12" height="12" rx="2" />
      <rect x="9.5" y="9.5" width="5" height="5" rx="1" />
      <path d="M9 3v3M15 3v3M9 18v3M15 18v3M3 9h3M3 15h3M18 9h3M18 15h3" />
    </svg>
  );
}

export function IconNetwork(props: IconProps) {
  return (
    <svg {...base(props)}>
      <circle cx="12" cy="12" r="2.4" />
      <circle cx="4.5" cy="6" r="1.8" />
      <circle cx="19.5" cy="6" r="1.8" />
      <circle cx="4.5" cy="18" r="1.8" />
      <circle cx="19.5" cy="18" r="1.8" />
      <path d="M6.1 7.1l3.8 3.2M17.9 7.1l-3.8 3.2M6.1 16.9l3.8-3.2M17.9 16.9l-3.8-3.2" />
    </svg>
  );
}

export function IconShield(props: IconProps) {
  return (
    <svg {...base(props)}>
      <path d="M12 3l7 2.6v5.2c0 4.4-2.9 8.1-7 10.2-4.1-2.1-7-5.8-7-10.2V5.6L12 3Z" />
      <path d="M9 12l2.1 2.1L15.4 10" />
    </svg>
  );
}

export function IconTerminal(props: IconProps) {
  return (
    <svg {...base(props)}>
      <rect x="2.5" y="4" width="19" height="16" rx="2.2" />
      <path d="M6.5 9.5l2.8 2.5-2.8 2.5M11.8 15h4.8" />
    </svg>
  );
}

export function IconWindow(props: IconProps) {
  return (
    <svg {...base(props)}>
      <rect x="2.5" y="4" width="19" height="16" rx="2.2" />
      <path d="M2.5 8.5h19M6 6.3h.01M8.5 6.3h.01M11 6.3h.01" />
      <path d="M6.5 12.5h6M6.5 16h9" />
    </svg>
  );
}

export function IconBolt(props: IconProps) {
  return (
    <svg {...base(props)}>
      <path d="M13 2.8L4.5 13.2h6L10.8 21l8.7-10.6h-6L13 2.8Z" />
    </svg>
  );
}

export function IconPlug(props: IconProps) {
  return (
    <svg {...base(props)}>
      <path d="M9 3v5M15 3v5M7 8h10v3.5a5 5 0 0 1-5 5 5 5 0 0 1-5-5V8ZM12 16.5V21" />
    </svg>
  );
}

export function IconArrowRight(props: IconProps) {
  return (
    <svg {...base(props)}>
      <path d="M4 12h15M13.5 6.5L19.5 12l-6 5.5" />
    </svg>
  );
}

export function IconArrowUpRight(props: IconProps) {
  return (
    <svg {...base(props)}>
      <path d="M7 17L17 7M8.5 7H17v8.5" />
    </svg>
  );
}

export function IconLock(props: IconProps) {
  return (
    <svg {...base(props)}>
      <rect x="4.5" y="10" width="15" height="10.5" rx="2.2" />
      <path d="M8 10V7.5a4 4 0 0 1 8 0V10M12 14v3" />
    </svg>
  );
}

export function IconLayers(props: IconProps) {
  return (
    <svg {...base(props)}>
      <path d="M12 3l9 4.8-9 4.8-9-4.8L12 3Z" />
      <path d="M3 12l9 4.8L21 12M3 16.2l9 4.8 9-4.8" />
    </svg>
  );
}

export function IconServer(props: IconProps) {
  return (
    <svg {...base(props)}>
      <rect x="3" y="4" width="18" height="7" rx="1.8" />
      <rect x="3" y="13" width="18" height="7" rx="1.8" />
      <path d="M6.5 7.5h.01M6.5 16.5h.01M10 7.5h6M10 16.5h6" />
    </svg>
  );
}

export function IconGithub({ className = "", size = 16 }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="currentColor"
      aria-hidden="true"
      className={className}
    >
      <path d="M12 .7A11.5 11.5 0 0 0 8.36 23.1c.58.1.79-.25.79-.56v-2c-3.2.7-3.88-1.37-3.88-1.37-.53-1.33-1.29-1.68-1.29-1.68-1.05-.72.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.77 2.7 1.26 3.36.96.1-.75.4-1.26.73-1.55-2.55-.29-5.24-1.28-5.24-5.7 0-1.26.45-2.29 1.19-3.1-.12-.3-.52-1.47.11-3.07 0 0 .97-.31 3.18 1.19a11 11 0 0 1 5.8 0c2.2-1.5 3.17-1.19 3.17-1.19.63 1.6.23 2.78.12 3.07.74.81 1.19 1.84 1.19 3.1 0 4.43-2.7 5.41-5.26 5.69.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.5 11.5 0 0 0 12 .7Z" />
    </svg>
  );
}

export function IconCopy({ className = "", size = 12 }: IconProps) {
  return (
    <svg {...base({ className, size })}>
      <rect x="8.5" y="8.5" width="12" height="12" rx="2" />
      <path d="M15.5 8.5v-2a2 2 0 0 0-2-2h-9a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h2" />
    </svg>
  );
}

export function IconCheck({ className = "", size = 12 }: IconProps) {
  return (
    <svg {...base({ className, size })}>
      <path d="M4.5 12.5l5 5 10-11" />
    </svg>
  );
}

export function IconSpark({ className = "", size = 14 }: IconProps) {
  return (
    <svg {...base({ className, size })}>
      <path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8L12 3Z" />
      <path d="M18.5 15.5l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8.8-2.2Z" />
    </svg>
  );
}
