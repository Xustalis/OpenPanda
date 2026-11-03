"use client";

import { useEffect } from "react";

/**
 * Adds `.is-in` to every `[data-reveal]` element as it enters the viewport.
 * The CSS keeps elements hidden until then, and a <noscript> rule in the
 * layout makes the content visible when JavaScript never runs.
 */
export default function RevealController() {
  useEffect(() => {
    const elements = Array.from(
      document.querySelectorAll<HTMLElement>("[data-reveal]"),
    );

    const prefersReduced = window.matchMedia(
      "(prefers-reduced-motion: reduce)",
    ).matches;

    if (prefersReduced || !("IntersectionObserver" in window)) {
      elements.forEach((element) => element.classList.add("is-in"));
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            entry.target.classList.add("is-in");
            observer.unobserve(entry.target);
          }
        });
      },
      { rootMargin: "0px 0px -8% 0px", threshold: 0.08 },
    );

    elements.forEach((element) => observer.observe(element));
    return () => observer.disconnect();
  }, []);

  return null;
}
