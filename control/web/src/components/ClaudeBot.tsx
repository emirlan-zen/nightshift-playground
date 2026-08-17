import { cn } from "@/lib/utils";

// Small geometric bot mascot. Idle: dim + still. Active (a session/job is
// running): purple, blinking eyes, pulsing antenna, gentle bob, and a scanning
// glimmer. Pure SVG + CSS keyframes (see index.css) — no runtime cost.
export function ClaudeBot({
  active = false,
  size = 26,
  className,
}: {
  active?: boolean;
  size?: number;
  className?: string;
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      fill="none"
      aria-hidden="true"
      className={cn(
        "shrink-0 transition-colors",
        active ? "text-accent animate-[bot-bob_2.4s_ease-in-out_infinite]" : "text-dim",
        className,
      )}
    >
      {/* antenna */}
      <line x1="16" y1="9" x2="16" y2="5" stroke="currentColor" strokeWidth="1.6" />
      <circle
        cx="16"
        cy="4"
        r="1.7"
        fill="currentColor"
        className={active ? "animate-[bot-pulse_1.1s_ease-in-out_infinite]" : ""}
      />
      {/* head */}
      <rect
        x="5.5"
        y="9.5"
        width="21"
        height="16"
        stroke="currentColor"
        strokeWidth="1.6"
        className="text-ink"
      />
      {/* eyes */}
      <g
        className={cn("bot-eyes", active && "animate-[bot-blink_3.2s_ease-in-out_infinite]")}
        fill="currentColor"
      >
        <rect x="10" y="15" width="3.2" height="3.2" />
        <rect x="18.8" y="15" width="3.2" height="3.2" />
      </g>
      {/* mouth */}
      <line
        x1="11"
        y1="21.5"
        x2="21"
        y2="21.5"
        stroke="currentColor"
        strokeWidth="1.4"
        className="text-mut"
      />
      {/* feet */}
      <line
        x1="11"
        y1="25.5"
        x2="11"
        y2="28"
        stroke="currentColor"
        strokeWidth="1.6"
        className="text-ink"
      />
      <line
        x1="21"
        y1="25.5"
        x2="21"
        y2="28"
        stroke="currentColor"
        strokeWidth="1.6"
        className="text-ink"
      />
    </svg>
  );
}

// Three "thinking" dots that ripple — for inline "working" affordances.
export function ThinkingDots({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex items-end gap-[3px]", className)}>
      {[0, 1, 2].map((i) => (
        <span
          key={i}
          className="size-[3px] bg-accent"
          style={{ animation: `bot-dot 1s ease-in-out ${i * 0.16}s infinite` }}
        />
      ))}
    </span>
  );
}
