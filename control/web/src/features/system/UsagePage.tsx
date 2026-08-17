import { type UsageSums } from "@/lib/api";
import { useUsage } from "@/lib/queries";
import { fmtInt, fmtTokens, fmtUSD } from "@/lib/format";
import { Card, Kicker } from "@/components/ui/card";
import { SectionHeader, EmptyText, ErrorText } from "@/components/SectionHeader";
import { cn } from "@/lib/utils";

const work = (s: UsageSums) => s.in + s.out;

function StatTile({
  label,
  value,
  sub,
  accent,
}: {
  label: string;
  value: string;
  sub?: string;
  accent?: boolean;
}) {
  return (
    <Card className="p-4">
      <Kicker>{label}</Kicker>
      <div
        className={cn(
          "mt-2 font-head text-[26px] font-bold leading-none tracking-tight tabular-nums",
          accent && "text-accent",
        )}
      >
        {value}
      </div>
      {sub && <div className="mt-1.5 font-mono text-[10.5px] text-dim">{sub}</div>}
    </Card>
  );
}

function DailyBars({
  days,
}: {
  days: { date: string; in: number; out: number; costUSD: number }[];
}) {
  const max = Math.max(1, ...days.map((d) => d.in + d.out));
  return (
    <div>
      <div className="flex h-[132px] items-end gap-[3px]">
        {days.map((d) => {
          const total = d.in + d.out;
          const h = (total / max) * 100;
          const outFrac = total ? d.out / total : 0;
          return (
            <div key={d.date} className="group relative flex h-full flex-1 flex-col justify-end">
              <div className="w-full bg-line2/40" style={{ height: `${h}%` }}>
                <div className="h-full w-full origin-bottom">
                  <div className="bg-dim" style={{ height: `${(1 - outFrac) * 100}%` }} />
                  <div className="bg-accent" style={{ height: `${outFrac * 100}%` }} />
                </div>
              </div>
              <div className="pointer-events-none absolute -top-6 left-1/2 z-10 hidden -translate-x-1/2 whitespace-nowrap border border-line2 bg-raise px-2 py-1 font-mono text-[10px] text-ink group-hover:block">
                {d.date.slice(5)} · {fmtTokens(total)} · {fmtUSD(d.costUSD)}
              </div>
            </div>
          );
        })}
      </div>
      <div className="mt-2 flex justify-between font-mono text-[9.5px] text-dim">
        <span>{days[0]?.date.slice(5)}</span>
        <span>{days[days.length - 1]?.date.slice(5)}</span>
      </div>
    </div>
  );
}

function BarList({
  rows,
}: {
  rows: { label: string; value: number; messages: number; costUSD: number }[];
}) {
  const max = Math.max(1, ...rows.map((r) => r.value));
  return (
    <div className="space-y-2.5">
      {rows.map((r) => (
        <div key={r.label}>
          <div className="mb-1 flex items-baseline justify-between gap-3">
            <span className="font-mono text-[12px] font-medium uppercase tracking-wider">
              {r.label}
            </span>
            <span className="font-mono text-[11px] text-mut tabular-nums">
              {fmtTokens(r.value)}{" "}
              <span className="text-dim">
                · {fmtUSD(r.costUSD)} · {fmtInt(r.messages)} turns
              </span>
            </span>
          </div>
          <div className="h-2 w-full bg-bg2">
            <div className="h-full bg-accent" style={{ width: `${(r.value / max) * 100}%` }} />
          </div>
        </div>
      ))}
    </div>
  );
}

export function Usage() {
  const { data, error, isLoading } = useUsage();

  if (isLoading) return <EmptyText>Loading usage…</EmptyText>;
  if (error instanceof Error) return <ErrorText>{error.message}</ErrorText>;
  if (!data) return null;

  const t = data.totals;
  const hasData = t.messages > 0;

  return (
    <section>
      <div className="mb-4 flex items-baseline justify-between gap-3">
        <Kicker>// token usage · last {data.windowDays} days · Asia/Bishkek</Kicker>
        <span className="font-mono text-[10px] text-dim">
          {fmtInt(t.messages)} turns · {fmtUSD(t.costUSD)}
        </span>
      </div>

      {!hasData ? (
        <EmptyText>No recorded sessions in the window.</EmptyText>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 md:grid-cols-5">
            <StatTile label="Input" value={fmtTokens(t.in)} sub="tokens sent" />
            <StatTile label="Output" value={fmtTokens(t.out)} sub="tokens generated" accent />
            <StatTile label="Cache read" value={fmtTokens(t.cacheRead)} sub="context reused" />
            <StatTile label="Cache write" value={fmtTokens(t.cacheCreate)} sub="context cached" />
            <StatTile label="Cost" value={fmtUSD(t.costUSD)} sub="API-equiv, all models" accent />
          </div>

          <SectionHeader className="mb-3 mt-8" title="Daily throughput" meta="input + output" />
          <Card className="p-4">
            <DailyBars days={data.days} />
            <div className="mt-3 flex items-center gap-4 font-mono text-[10px] text-dim">
              <span className="flex items-center gap-1.5">
                <span className="size-2.5 bg-accent" /> output
              </span>
              <span className="flex items-center gap-1.5">
                <span className="size-2.5 bg-dim" /> input
              </span>
            </div>
          </Card>

          <SectionHeader className="mb-3 mt-8" title="By model" meta="input + output" />
          <Card className="p-4">
            <BarList
              rows={data.models.map((m) => ({
                label: m.model,
                value: work(m),
                messages: m.messages,
                costUSD: m.costUSD,
              }))}
            />
          </Card>

          <SectionHeader className="mb-3 mt-8" title="By agent" meta="input + output" />
          <Card className="p-4">
            <BarList
              rows={data.agents.map((a) => ({
                label: a.agent,
                value: work(a),
                messages: a.messages,
                costUSD: a.costUSD,
              }))}
            />
          </Card>
        </>
      )}

      <div className="mt-8 max-w-[64ch] text-[11px] leading-relaxed text-dim">
        Aggregated from each agent's Claude Code session logs (token counts only, no transcript
        content). Cache reads are cheap context reuse and dwarf raw input, so throughput charts
        count input + output.
      </div>
    </section>
  );
}
