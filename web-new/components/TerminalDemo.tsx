"use client";

import { useEffect, useMemo, useState } from "react";

type Tone = "dim" | "muted" | "jade" | "cyan" | "amber" | "white" | "comment";

type Segment = { text: string; tone?: Tone };

type Step = { cmd: string; output: Segment[][] };

const seg = (text: string, tone?: Tone): Segment => ({ text, tone });

const STEPS: Step[] = [
  {
    cmd: "panda status",
    output: [
      [seg("  node     "), seg("darwin-arm64", "white"), seg(" · 12 cores")],
      [seg("  role     "), seg("orchestrator / executor", "muted")],
      [seg("  agents   "), seg("claude_code codex opencode", "jade")],
      [seg("  models   "), seg("3 registered · 3 healthy", "muted")],
      [seg("  uptime   "), seg("6h 12m", "dim")],
    ],
  },
  {
    cmd: "panda nodes",
    output: [
      [seg("  ● ", "jade"), seg("darwin-arm64   mesh     load 0.31", "muted")],
      [seg("  ● ", "jade"), seg("linux-gpu      mesh     load 0.08", "muted")],
      [seg("  ● ", "amber"), seg("orangepi-3b    guarded  load 0.02", "muted")],
      [
        seg("  3 nodes online ", "white"),
        seg("· 0 cloud relays", "dim"),
      ],
    ],
  },
  {
    cmd: "panda queue",
    output: [
      [
        seg("  #1042  ", "dim"),
        seg("running   refactor-auth      ", "white"),
        seg("→ linux-gpu", "cyan"),
      ],
      [
        seg("  #1043  ", "dim"),
        seg("queued    release-notes      ", "muted"),
        seg("→ darwin-arm64", "cyan"),
      ],
      [
        seg("  #1044  ", "dim"),
        seg("review    bump-lockfile      ", "muted"),
        seg("! tier-2 approval", "amber"),
      ],
    ],
  },
];

const toneClass: Record<Tone, string> = {
  dim: "t-dim",
  muted: "t-muted",
  jade: "t-jade",
  cyan: "t-cyan",
  amber: "t-amber",
  white: "t-white",
  comment: "comment",
};

function OutputLine({ segments }: { segments: Segment[] }) {
  return (
    <div className="term-line">
      {segments.map((segment, index) => (
        <span key={index} className={segment.tone ? toneClass[segment.tone] : undefined}>
          {segment.text}
        </span>
      ))}
    </div>
  );
}

const REDUCED_LABEL = "终端演示";
const srTranscript = STEPS.map((step) => step.cmd).join("，");

export default function TerminalDemo() {
  const [reduced, setReduced] = useState(false);
  const [stepIndex, setStepIndex] = useState(0);
  const [chars, setChars] = useState(0);
  const [shown, setShown] = useState(0);
  const [phase, setPhase] = useState<"typing" | "output">("typing");

  useEffect(() => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    setReduced(query.matches);
    const onChange = (event: MediaQueryListEvent) => setReduced(event.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);

  useEffect(() => {
    if (reduced) return;

    if (stepIndex >= STEPS.length) {
      const reset = window.setTimeout(() => {
        setStepIndex(0);
        setChars(0);
        setShown(0);
        setPhase("typing");
      }, 6200);
      return () => window.clearTimeout(reset);
    }

    const step = STEPS[stepIndex];

    if (phase === "typing") {
      if (chars < step.cmd.length) {
        const character = step.cmd[chars];
        const delay = character === " " ? 70 : 26 + Math.random() * 34;
        const timer = window.setTimeout(() => setChars((value) => value + 1), delay);
        return () => window.clearTimeout(timer);
      }
      const timer = window.setTimeout(() => setPhase("output"), 400);
      return () => window.clearTimeout(timer);
    }

    if (shown < step.output.length) {
      const timer = window.setTimeout(() => setShown((value) => value + 1), 100);
      return () => window.clearTimeout(timer);
    }

    const timer = window.setTimeout(() => {
      setStepIndex((value) => value + 1);
      setChars(0);
      setShown(0);
      setPhase("typing");
    }, 900);
    return () => window.clearTimeout(timer);
  }, [chars, phase, reduced, shown, stepIndex]);

  const completeSteps = useMemo(
    () => (reduced ? STEPS.length : stepIndex),
    [reduced, stepIndex],
  );

  const current = !reduced && stepIndex < STEPS.length ? STEPS[stepIndex] : null;
  const typed = current ? current.cmd.slice(0, reduced ? current.cmd.length : chars) : "";

  return (
    <figure className="relative">
      <div className="terminal" aria-hidden="true">
        <div className="terminal-bar">
          <span className="traffic red" />
          <span className="traffic amber" />
          <span className="traffic green" />
          <span className="terminal-title">panda@mesh — zsh</span>
          <span className="terminal-badge">
            <i />
            local
          </span>
        </div>
        <div className="terminal-body" aria-hidden="true">
          {STEPS.slice(0, completeSteps).map((step, index) => (
            <div key={`done-${index}`}>
              <div className="term-line">
                <span className="prompt">$ </span>
                <span className="cmd">{step.cmd}</span>
              </div>
              {step.output.map((segments, lineIndex) => (
                <OutputLine key={lineIndex} segments={segments} />
              ))}
              <div className="term-line" />
            </div>
          ))}

          {current && (
            <div>
              <div className="term-line">
                <span className="prompt">$ </span>
                <span className="cmd">{typed}</span>
                <span className="cursor" />
              </div>
              {phase === "output" &&
                current.output.slice(0, shown).map((segments, lineIndex) => (
                  <OutputLine key={lineIndex} segments={segments} />
                ))}
            </div>
          )}

          {reduced && (
            <div className="term-line">
              <span className="prompt">$ </span>
              <span className="cursor" />
            </div>
          )}
        </div>
      </div>

      <div className="sr-only">
        {REDUCED_LABEL}：依次执行 {srTranscript}。三台节点在线，其中一台处于 guarded 信任档；
        队列中两个任务执行中或排队，一个进入 tier-2 人工审批。
      </div>
    </figure>
  );
}
