"use client";

import { useEffect, useRef } from "react";

type Star = {
  x: number;
  y: number;
  radius: number;
  alpha: number;
  phase: number;
  twinkle: number;
  driftX: number;
  driftY: number;
  crossSize: number;
};

const INK = "58, 51, 42";
const MAX_STARS = 150;

function draw(
  ctx: CanvasRenderingContext2D,
  stars: Star[],
  width: number,
  height: number,
  time: number,
) {
  ctx.clearRect(0, 0, width, height);

  for (const star of stars) {
    const pulse = 1 + Math.sin(time * star.twinkle + star.phase) * 0.32;
    const alpha = Math.min(0.48, star.alpha * pulse);
    const x = star.x + Math.sin(time * star.driftX + star.phase) * 2.2;
    const y = star.y + Math.cos(time * star.driftY + star.phase * 1.7) * 1.6;

    if (star.crossSize > 0) {
      ctx.strokeStyle = `rgba(${INK}, ${alpha * 0.9})`;
      ctx.lineWidth = 0.75;
      ctx.beginPath();
      ctx.moveTo(x - star.crossSize, y);
      ctx.lineTo(x + star.crossSize, y);
      ctx.moveTo(x, y - star.crossSize);
      ctx.lineTo(x, y + star.crossSize);
      ctx.stroke();
    } else {
      ctx.fillStyle = `rgba(${INK}, ${alpha})`;
      ctx.beginPath();
      ctx.arc(x, y, star.radius, 0, Math.PI * 2);
      ctx.fill();
    }
  }
}

/**
 * A sparse ink-dot star map drawn behind a section. Stars drift and twinkle
 * slowly; with `prefers-reduced-motion` they are drawn once and left still.
 */
export default function Starfield({ className = "" }: { className?: string }) {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    const ctx = canvas?.getContext("2d");
    if (!canvas || !ctx) return;

    const motionQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
    let reduced = motionQuery.matches;
    let stars: Star[] = [];
    let width = 0;
    let height = 0;
    let frame: number | null = null;

    const measure = () => {
      const rect = canvas.getBoundingClientRect();
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      width = rect.width;
      height = rect.height;
      canvas.width = Math.max(1, Math.round(width * dpr));
      canvas.height = Math.max(1, Math.round(height * dpr));
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    };

    const build = () => {
      if (width === 0 || height === 0) return;
      const count = Math.min(
        MAX_STARS,
        Math.max(18, Math.round((width * height) / 15000)),
      );
      stars = Array.from({ length: count }, () => {
        const cross = Math.random() < 0.1;
        return {
          x: Math.random() * width,
          y: Math.random() * height,
          radius: 0.45 + Math.random() * 0.75,
          alpha: 0.12 + Math.random() * 0.22,
          phase: Math.random() * Math.PI * 2,
          twinkle: 0.4 + Math.random() * 0.9,
          driftX: 0.06 + Math.random() * 0.16,
          driftY: 0.05 + Math.random() * 0.14,
          crossSize: cross ? 2 + Math.random() * 2 : 0,
        };
      });
    };

    const paint = (time: number) => {
      draw(ctx, stars, width, height, time);
    };

    const stop = () => {
      if (frame !== null) {
        cancelAnimationFrame(frame);
        frame = null;
      }
    };

    const loop = (now: number) => {
      paint(now / 1000);
      frame = requestAnimationFrame(loop);
    };

    const sync = () => {
      stop();
      if (reduced) {
        paint(0);
      } else {
        frame = requestAnimationFrame(loop);
      }
    };

    measure();
    build();
    sync();

    const onResize = () => {
      measure();
      build();
      if (reduced) paint(0);
    };
    const onMotionChange = () => {
      reduced = motionQuery.matches;
      sync();
    };

    const observer = new ResizeObserver(onResize);
    observer.observe(canvas);
    motionQuery.addEventListener("change", onMotionChange);

    return () => {
      stop();
      observer.disconnect();
      motionQuery.removeEventListener("change", onMotionChange);
    };
  }, []);

  return <canvas ref={canvasRef} className={`starfield ${className}`} aria-hidden="true" />;
}
