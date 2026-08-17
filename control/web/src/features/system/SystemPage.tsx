import { useNavigate } from "react-router-dom";
import {
  Activity,
  ArrowRight,
  CalendarClock,
  Eye,
  EyeOff,
  FileCode2,
  Focus as FocusIcon,
  HeartPulse,
  ListChecks,
  Radio,
  Settings2,
  TicketCheck,
  WalletCards,
} from "lucide-react";
import { Card, Kicker } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { useHealth, useObs, useProposals, useServers, useServersUnfiltered } from "@/lib/queries";
import {
  baseAgent,
  isAgentVisible,
  setAgentHidden,
  setPresenting,
  useVisibility,
} from "@/lib/visibility";

const groups = [
  {
    title: "Operate",
    items: [
      {
        to: "/sessions",
        label: "Manual sessions",
        desc: "Start, name, and stop remote-control sessions.",
        icon: Radio,
      },
      {
        to: "/health",
        label: "Health",
        desc: "Machine, Claude, forge tokens, and observability alerts.",
        icon: HeartPulse,
      },
      {
        to: "/usage",
        label: "Usage",
        desc: "Subscription throughput and API-equivalent spend.",
        icon: Activity,
      },
    ],
  },
  {
    title: "Automation",
    items: [
      {
        to: "/automations",
        label: "Automation studio",
        desc: "Templates, node prompts, and recurring schedules.",
        icon: CalendarClock,
      },
      {
        to: "/runs",
        label: "Run history",
        desc: "Every run, night sweep, report, and wave timeline.",
        icon: ListChecks,
      },
      {
        to: "/tickets",
        label: "Tickets",
        desc: "Cross-run follow-ups and operator review queue.",
        icon: TicketCheck,
      },
    ],
  },
  {
    title: "Direction & internals",
    items: [
      {
        to: "/focus",
        label: "Direction",
        desc: "Persistent product and project north stars.",
        icon: FocusIcon,
      },
      {
        to: "/prompts",
        label: "Prompt library",
        desc: "Contracts, node prompts, agent guidance, and settings.",
        icon: FileCode2,
      },
    ],
  },
];

// Presentation-mode settings: which agents stay on screen when the eye in the
// top bar is switched off. Reads the UNfiltered server list — hidden agents
// must stay listed here or there would be no way to unhide them.
function VisibilityCard() {
  const vis = useVisibility();
  const { data: servers = [] } = useServersUnfiltered();
  const agents = [...new Set(servers.map((s) => baseAgent(s.company)))].sort();
  const ModeIcon = vis.presenting ? EyeOff : Eye;

  return (
    <div>
      <div className="mb-2 font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-mut">
        Visibility
      </div>
      <Card className="px-4 py-1">
        <div className="flex w-full items-center gap-3 border-b border-line/60 py-3.5">
          <ModeIcon className={`size-4 shrink-0 ${vis.presenting ? "text-accent" : "text-mut"}`} />
          <div className="min-w-0 flex-1">
            <div className="text-[13.5px] font-semibold">Presentation mode</div>
            <div className="mt-0.5 text-[11px] leading-relaxed text-dim">
              Hide private agents everywhere — sessions, runs, tickets, inbox, usage — like hiding
              balances in a banking app. The eye in the top bar toggles this too.
            </div>
          </div>
          <Switch
            checked={vis.presenting}
            onCheckedChange={setPresenting}
            aria-label="Presentation mode"
          />
        </div>
        {agents.map((agent) => {
          const visible = isAgentVisible(agent, { ...vis, presenting: true });
          return (
            <div
              key={agent}
              className="flex w-full items-center gap-3 border-b border-line/60 py-3 last:border-0"
            >
              <span className="min-w-0 flex-1 font-mono text-[12px]">{agent}</span>
              <span className="font-mono text-[9.5px] uppercase tracking-wider text-dim">
                {visible ? "shown" : "hidden when presenting"}
              </span>
              <Switch
                checked={visible}
                onCheckedChange={(on) => setAgentHidden(agent, !on)}
                aria-label={`Show ${agent} while presenting`}
              />
            </div>
          );
        })}
      </Card>
    </div>
  );
}

export function System() {
  const navigate = useNavigate();
  const { data: servers = [] } = useServers();
  const { data: health } = useHealth();
  const { data: obs } = useObs();
  const { data: proposals = [] } = useProposals();
  const live = servers.flatMap((s) => s.sessions).filter((s) => s.active).length;
  const issues =
    (obs?.alerts.length ?? 0) +
    (health?.auth.ok === false ? 1 : 0) +
    (health?.forge?.filter((f) => !f.ok).length ?? 0);

  return (
    <section>
      <div className="mb-5">
        <Kicker>// system</Kicker>
        <p className="mt-1.5 max-w-[58ch] text-[12.5px] leading-relaxed text-mut">
          Health, usage, history, and compatibility surfaces. Everyday work stays in Sessions, Runs,
          Automations, and Inbox.
        </p>
      </div>

      <div className="mb-6 grid grid-cols-1 gap-2 sm:grid-cols-3">
        <Card className="flex items-center gap-3 p-3.5">
          <Radio className="size-4 text-ok" />
          <div className="min-w-0 flex-1">
            <div className="font-head text-[15px] font-bold">{live}</div>
            <div className="font-mono text-[9.5px] uppercase text-dim">manual sessions</div>
          </div>
        </Card>
        <Card className="flex items-center gap-3 p-3.5">
          <HeartPulse className={`size-4 ${issues ? "text-danger" : "text-ok"}`} />
          <div className="min-w-0 flex-1">
            <div className="font-head text-[15px] font-bold">{issues || "Clear"}</div>
            <div className="font-mono text-[9.5px] uppercase text-dim">system issues</div>
          </div>
        </Card>
        <Card className="flex items-center gap-3 p-3.5">
          <WalletCards className="size-4 text-accent" />
          <div className="min-w-0 flex-1">
            <div className="font-head text-[15px] font-bold">{proposals.length}</div>
            <div className="font-mono text-[9.5px] uppercase text-dim">automation proposals</div>
          </div>
        </Card>
      </div>

      <div className="space-y-7">
        {groups.map((g) => (
          <div key={g.title}>
            <div className="mb-2 font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-mut">
              {g.title}
            </div>
            <Card className="px-4 py-1">
              {g.items.map((item) => {
                const Icon = item.icon;
                return (
                  <button
                    key={item.to}
                    onClick={() => navigate(item.to)}
                    className="flex w-full items-center gap-3 border-b border-line/60 py-3.5 text-left last:border-0 hover:opacity-75"
                  >
                    <Icon className="size-4 shrink-0 text-mut" />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 text-[13.5px] font-semibold">
                        {item.label}
                        {item.to === "/automations" && proposals.length > 0 ? (
                          <Badge tone="review">{proposals.length}</Badge>
                        ) : null}
                      </div>
                      <div className="mt-0.5 text-[11px] leading-relaxed text-dim">{item.desc}</div>
                    </div>
                    <ArrowRight className="size-4 shrink-0 text-dim" />
                  </button>
                );
              })}
            </Card>
          </div>
        ))}
        <VisibilityCard />
      </div>

      <div className="mt-8 flex items-center gap-2 font-mono text-[9.5px] uppercase tracking-wider text-dim">
        <Settings2 className="size-3.5" /> Existing APIs and deep links remain available; this is an
        information-architecture change, not a capability removal.
      </div>
    </section>
  );
}
