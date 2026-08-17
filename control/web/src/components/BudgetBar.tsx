import type { ObsBudget } from "@/lib/api";
import { fmtUSD } from "@/lib/format";

// Governor budget pace (ADR-0012): tonight's API-equivalent spend against the
// (auto-ratcheting) budget, plus top-ups minted and the trailing-7-day burn.
// Presentational — used by the Health tab.
export function BudgetBar({ b }: { b: ObsBudget }) {
  const pct = b.nightBudgetUSD > 0 ? (b.nightSpendUSD / b.nightBudgetUSD) * 100 : 0;
  const over = pct >= 100;
  return (
    <div>
      <div className="mb-2 flex items-baseline justify-between gap-3">
        <span className="font-head text-[19px] font-bold tracking-tight tabular-nums">
          {fmtUSD(b.nightSpendUSD)}
          <span className="text-dim"> / {fmtUSD(b.nightBudgetUSD)}</span>
        </span>
        <span className="font-mono text-[11px] text-mut tabular-nums">{Math.round(pct)}%</span>
      </div>
      <div className="h-2 w-full bg-bg2">
        <div
          className={over ? "h-full bg-ok" : "h-full bg-accent"}
          style={{ width: `${Math.min(100, Math.max(2, pct))}%` }}
        />
      </div>
      <div className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10.5px] text-dim">
        <span>
          {b.topUpsMinted} top-up{b.topUpsMinted === 1 ? "" : "s"} tonight
        </span>
        <span>{fmtUSD(b.weekSpendUSD)} · trailing 7d</span>
      </div>
      {b.lastAdjust && (
        <div className="mt-1 font-mono text-[10.5px] text-dim">
          auto-adjusted {b.lastAdjust}
          {b.limitHitNight
            ? ` · ceiling ${fmtUSD(b.spendAtHitUSD ?? 0)} @ limit-hit ${b.limitHitNight}`
            : ""}
        </div>
      )}
    </div>
  );
}
