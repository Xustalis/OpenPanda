"use client";

import { useEffect, useRef, useState } from "react";
import { IconCheck, IconCopy } from "./Icons";

type CopyButtonProps = {
  value: string;
  label?: string;
  copiedLabel?: string;
};

export default function CopyButton({
  value,
  label = "复制",
  copiedLabel = "已复制",
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), 1800);
    } catch {
      // Clipboard access can be denied; leave the button unchanged.
    }
  }

  return (
    <button
      type="button"
      onClick={handleCopy}
      className={`icon-btn ${copied ? "copied" : ""}`}
      aria-label={copied ? copiedLabel : `${label}：${value}`}
    >
      {copied ? <IconCheck /> : <IconCopy />}
      <span>{copied ? copiedLabel : label}</span>
    </button>
  );
}
