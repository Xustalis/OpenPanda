"use client";

import { useEffect, useRef, useState } from "react";
import { IconArrowUpRight, IconGithub } from "./Icons";

export type MenuLink = { href: string; label: string; index: string };

type MobileMenuProps = {
  links: MenuLink[];
  github: string;
};

export default function MobileMenu({ links, github }: MobileMenuProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };

    document.addEventListener("keydown", onKeyDown);
    document.addEventListener("pointerdown", onPointerDown);
    return () => {
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("pointerdown", onPointerDown);
    };
  }, [open]);

  return (
    <div className="mobile-menu" ref={rootRef}>
      <button
        type="button"
        className="menu-toggle"
        aria-expanded={open}
        aria-controls="mobile-nav"
        onClick={() => setOpen((value) => !value)}
      >
        <span>菜单</span>
        <span className="menu-bars" aria-hidden="true">
          <span />
          <span />
        </span>
      </button>

      {open && (
        <div className="mobile-menu-panel" id="mobile-nav">
          {links.map((link) => (
            <a key={link.href} href={link.href} onClick={() => setOpen(false)}>
              <span>{link.label}</span>
              <span className="menu-index">{link.index}</span>
            </a>
          ))}
          <a
            href={github}
            target="_blank"
            rel="noreferrer noopener"
            onClick={() => setOpen(false)}
          >
            <span className="inline-flex items-center gap-1.5">
              <IconGithub size={13} /> GitHub
            </span>
            <IconArrowUpRight size={12} />
          </a>
          <div className="menu-foot">LOCAL-FIRST · NO CLOUD RELAY</div>
        </div>
      )}
    </div>
  );
}
