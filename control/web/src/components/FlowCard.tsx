import { ArrowRight, Check, Circle, LoaderCircle, ShieldAlert } from "lucide-react";
import type { Flow, FlowStatus } from "@/lib/api";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { cn } from "@/lib/utils";

const statusTone: Record<FlowStatus, BadgeProps["tone"]> = {
  queued: "sched",
  running: "accent",
  complete: "ok",
  blocked: "danger",
  deadline: "review",
  stopped: "done",
};

function nodeIcon(state: Flow["nodeViews"][number]["state"]) {
  if (state === "delivered") return Check;
  if (state === "running") return LoaderCircle;
  if (state === "skipped" || state === "stopped") return ShieldAlert;
  return Circle;
}

export function FlowCard({
  flow,
  onOpen,
  compact = false,
}: {
  flow: Flow;
  onOpen: () => void;
  compact?: boolean;
}) {
  const delivered = flow.nodeViews.filter((n) => n.state === "delivered").length;
  const current =
    flow.nodeViews.find((n) => n.state === "running") ??
    flow.nodeViews.find((n) => n.state === "queued" || n.state === "waiting");
  return (
    <button
      onClick={onOpen}
      className="block w-full min-w-0 max-w-full overflow-hidden text-left transition-opacity hover:opacity-80"
    >
      <Card className={cn("min-w-0 max-w-full overflow-hidden p-4", compact && "p-3.5")}>
        <div className="flex items-start gap-3">
          <div className="min-w-0 flex-1">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <Badge tone={statusTone[flow.status]} dot={flow.status === "running"}>
                {flow.status}
              </Badge>
              <span className="font-mono text-[10px] uppercase tracking-wider text-dim">
                {flow.agent} / {flow.repo}
              </span>
            </div>
            <div className="font-head text-[16px] font-bold leading-snug tracking-tight">
              {flow.goal}
            </div>
            <div className="mt-2 font-mono text-[10.5px] text-mut">
              {current ? `${current.name.toLowerCase()} · ` : ""}
              {delivered}/{flow.nodeViews.length} stages
              {flow.deadline
                ? ` · by ${new Date(flow.deadline).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}`
                : " · no deadline"}
            </div>
          </div>
          <ArrowRight className="mt-1 size-4 shrink-0 text-dim" />
        </div>
        {!compact && (
          <div className="mt-4 flex items-center gap-1.5 overflow-hidden">
            {flow.nodeViews.map((n) => {
              const Icon = nodeIcon(n.state);
              return (
                <div key={n.jobId} className="flex min-w-0 flex-1 items-center gap-1.5">
                  <Icon
                    className={cn(
                      "size-3.5 shrink-0",
                      n.state === "delivered"
                        ? "text-ok"
                        : n.state === "running"
                          ? "animate-spin text-accent"
                          : n.state === "skipped"
                            ? "text-danger"
                            : "text-dim",
                    )}
                  />
                  <span className="truncate font-mono text-[9.5px] uppercase tracking-wide text-mut">
                    {n.name}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </Card>
    </button>
  );
}
