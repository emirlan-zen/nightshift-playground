import { EyeOff } from "lucide-react";

/** Deep-link guard for presentation mode: list surfaces filter hidden agents
 * centrally, but detail routes (/night/:agent/:id, /runs/:id) fetch by id and
 * would still render one. Pair with useAgentHidden from lib/visibility. */
export function HiddenAgentNotice() {
  return (
    <section className="flex flex-col items-center gap-3 border border-line bg-surface px-4 py-14 text-center">
      <EyeOff className="size-5 text-dim" />
      <div className="font-mono text-[11px] uppercase tracking-wider text-mut">
        Hidden while presenting
      </div>
      <p className="max-w-[44ch] text-[12px] leading-relaxed text-dim">
        This page belongs to a private agent. Turn presentation mode off (the eye in the top bar) to
        view it.
      </p>
    </section>
  );
}
