import { Link, Outlet, useLocation } from "react-router-dom";
import { useEffect } from "react";
import {
  Activity,
  Blocks,
  Compass,
  Eye,
  EyeOff,
  Home,
  Inbox,
  PlaySquare,
  Radio,
  Settings2,
} from "lucide-react";
import { useServers, useTickets, useObs, useProposals, useFlows, useHealth } from "@/lib/queries";
import { setPresenting, useVisibility } from "@/lib/visibility";
import { confirmNav } from "@/lib/navguard";
import { ClaudeBot } from "@/components/ClaudeBot";
import { IncidentStrip } from "@/components/IncidentStrip";
import { cn } from "@/lib/utils";

type NavItem = {
  label: string;
  to: string;
  icon: typeof Home;
  match: string[];
  count?: number;
};

function NavCount({ n, active }: { n?: number; active: boolean }) {
  if (!n) return null;
  return (
    <span
      className={cn(
        "ml-auto min-w-5 px-1.5 py-0.5 text-center font-mono text-[10px] font-bold",
        active ? "bg-accent text-ink-on-accent" : "bg-raise text-ink",
      )}
    >
      {n}
    </span>
  );
}

function isPathActive(pathname: string, item: NavItem) {
  return item.match.some((path) => pathname === path || pathname.startsWith(path + "/"));
}

function Crumbs({ pathname }: { pathname: string }) {
  const parts: { label: string; to?: string }[] = [];
  const seg = pathname.split("/").filter(Boolean);
  const leaf = seg.at(-1) ?? "";
  if (pathname === "/home") return <span className="text-dim">Overview</span>;

  if (
    pathname.startsWith("/runs") ||
    pathname.startsWith("/flows") ||
    pathname.startsWith("/night")
  ) {
    parts.push({ label: "Runs", to: "/runs" });
    if (leaf && leaf !== "runs" && leaf !== "flows" && leaf !== "night")
      parts.push({ label: leaf === "new" ? "New run" : leaf.replace(/^flow-/, "") });
  } else if (pathname.startsWith("/sessions") || pathname.startsWith("/servers")) {
    parts.push({ label: "Sessions" });
  } else if (
    pathname.startsWith("/automations") ||
    pathname.startsWith("/pipeline") ||
    pathname.startsWith("/prompts")
  ) {
    parts.push({ label: "Automations", to: "/automations" });
    if (seg.includes("templates")) parts.push({ label: "Templates", to: "/automations/templates" });
    if (seg.includes("nodes")) parts.push({ label: "Node library", to: "/automations/nodes" });
    if (seg.includes("profiles") || pathname.startsWith("/pipeline"))
      parts.push({ label: "Schedules", to: "/automations" });
    if (
      leaf &&
      !["automations", "templates", "nodes", "profiles", "pipeline", "prompts"].includes(leaf)
    )
      parts.push({ label: decodeURIComponent(leaf).replace(/^node-/, "") });
  } else {
    const labels: Record<string, string> = {
      inbox: "Inbox",
      focus: "Direction",
      system: "System",
      health: "Health",
      usage: "Usage",
      tickets: "Tickets",
    };
    parts.push({ label: labels[seg[0]] ?? seg[0] ?? "Overview" });
    if (seg.length > 1) parts.push({ label: decodeURIComponent(leaf) });
  }

  return (
    <>
      {parts.map((part, index) => (
        <span key={`${part.label}-${index}`} className="inline-flex items-center gap-2">
          {index > 0 && <span className="text-line2">/</span>}
          {part.to && index < parts.length - 1 ? (
            <Link className="text-mut transition-colors hover:text-ink" to={part.to}>
              {part.label}
            </Link>
          ) : (
            <span className={index === parts.length - 1 ? "text-ink" : "text-mut"}>
              {part.label}
            </span>
          )}
        </span>
      ))}
    </>
  );
}

// Banking-app visibility toggle: one tap hides every private-agent surface for
// showing the workbench to outsiders. Lives in the sticky crumb bar so it is
// reachable on every page and screen size.
function PresentationToggle() {
  const { presenting } = useVisibility();
  const Icon = presenting ? EyeOff : Eye;
  return (
    <button
      type="button"
      aria-pressed={presenting}
      aria-label={presenting ? "Show hidden agents" : "Hide private agents"}
      title={presenting ? "Presentation mode on — private agents hidden" : "Hide private agents"}
      onClick={() => setPresenting(!presenting)}
      className={cn(
        "flex min-h-11 items-center gap-2 px-2 font-mono text-[9.5px] uppercase tracking-wider transition-colors",
        presenting ? "text-accent" : "text-dim hover:text-ink",
      )}
    >
      <Icon className="size-4" />
      <span className="hidden sm:inline">{presenting ? "Presenting" : ""}</span>
    </button>
  );
}

export function AppShell() {
  const { pathname } = useLocation();
  useEffect(() => {
    window.scrollTo(0, 0);
  }, [pathname]);

  const { data: servers = [] } = useServers();
  const { data: tickets = [] } = useTickets();
  const { data: obs } = useObs();
  const { data: proposals = [] } = useProposals();
  const { data: flows = [] } = useFlows();
  const { data: health } = useHealth();

  const liveSessions = servers.flatMap((s) => s.sessions ?? []).filter((s) => s.active).length;
  const activeRuns = flows.filter((f) => f.status === "running" || f.status === "queued").length;
  const blockedRuns = flows.filter((f) => f.status === "blocked" || f.status === "deadline").length;
  const reviewTickets = tickets
    .flatMap((g) => g.tickets ?? [])
    .filter((t) => t.status === "review").length;
  const healthIssues =
    (health?.auth.checkedAt && !health.auth.ok ? 1 : 0) +
    (health?.recentFailures?.length ?? 0) +
    (health?.forge?.filter((f) => !f.ok).length ?? 0) +
    (health?.depSkips?.length ?? 0);
  const inboxCount =
    blockedRuns + reviewTickets + proposals.length + (obs?.alerts.length ?? 0) + healthIssues;

  const primary: NavItem[] = [
    { label: "Overview", to: "/home", icon: Home, match: ["/home"] },
    {
      label: "Sessions",
      to: "/sessions",
      icon: Radio,
      match: ["/sessions", "/servers"],
      count: liveSessions,
    },
    {
      label: "Runs",
      to: "/runs",
      icon: PlaySquare,
      match: ["/runs", "/flows", "/night"],
      count: activeRuns,
    },
    {
      label: "Automations",
      to: "/automations",
      icon: Blocks,
      match: ["/automations", "/pipeline", "/prompts"],
    },
    { label: "Inbox", to: "/inbox", icon: Inbox, match: ["/inbox", "/tickets"], count: inboxCount },
  ];
  const secondary: NavItem[] = [
    { label: "Direction", to: "/focus", icon: Compass, match: ["/focus"] },
    { label: "System", to: "/system", icon: Settings2, match: ["/system", "/health", "/usage"] },
  ];

  const navLink = (item: NavItem, mobile = false) => {
    const active = isPathActive(pathname, item);
    const Icon = item.icon;
    return (
      <Link
        key={item.to}
        to={item.to}
        onClick={(e) => {
          if (!confirmNav()) e.preventDefault();
        }}
        className={cn(
          mobile
            ? "relative flex min-w-0 flex-1 flex-col items-center justify-center gap-1 px-1 py-2 font-mono text-[9px] font-bold uppercase tracking-wide"
            : "group flex min-h-11 items-center gap-3 border-l-2 px-3 py-2.5 text-[13px] font-semibold transition-colors",
          active
            ? mobile
              ? "text-accent"
              : "border-accent bg-accent/7 text-ink"
            : mobile
              ? "text-mut"
              : "border-transparent text-mut hover:bg-surface hover:text-ink",
        )}
      >
        <Icon className={mobile ? "size-[18px]" : "size-4"} />
        <span>{mobile && item.label === "Automations" ? "Build" : item.label}</span>
        {mobile ? (
          item.count ? (
            <span className="absolute right-[calc(50%-17px)] top-1 min-w-4 bg-raise px-1 text-center text-[8px] text-ink">
              {item.count}
            </span>
          ) : null
        ) : (
          <NavCount n={item.count} active={active} />
        )}
      </Link>
    );
  };

  return (
    <div className="min-h-dvh bg-bg lg:grid lg:grid-cols-[236px_minmax(0,1fr)]">
      <h1 className="sr-only">nightshift Workbench</h1>
      <aside className="sticky top-0 hidden h-dvh flex-col border-r border-line bg-bg2 p-4 lg:flex">
        <Link to="/home" className="mb-7 flex items-center gap-3 px-2 py-1">
          <span className="size-3 bg-accent" />
          <span>
            <span className="block font-head text-[20px] font-bold leading-none tracking-tight">
              nightshift
            </span>
            <span className="mt-1.5 block font-mono text-[9px] uppercase tracking-[0.16em] text-dim">
              Workbench
            </span>
          </span>
        </Link>
        <nav className="space-y-1" aria-label="Primary navigation">
          {primary.map((item) => navLink(item))}
        </nav>
        <div className="mt-auto border-t border-line pt-4">
          <nav className="space-y-1" aria-label="Secondary navigation">
            {secondary.map((item) => navLink(item))}
          </nav>
          <div className="mt-4 flex items-center gap-2 border border-line bg-bg px-3 py-2.5 font-mono text-[10px] uppercase tracking-wider text-mut">
            <ClaudeBot active={liveSessions > 0} size={23} />
            <span>{liveSessions ? `${liveSessions} live` : "all idle"}</span>
          </div>
        </div>
      </aside>

      <div className="min-w-0">
        <header className="flex h-[58px] items-center gap-3 border-b border-line px-4 lg:hidden">
          <span className="size-2.5 bg-accent" />
          <div className="font-head text-[19px] font-bold tracking-tight">nightshift</div>
          {/* Direction/System live only in the desktop sidebar's secondary nav —
              on the phone these header icons are their one entry point. */}
          <nav className="ml-auto flex items-center" aria-label="Secondary navigation">
            {secondary.map((item) => {
              const Icon = item.icon;
              return (
                <Link
                  key={item.to}
                  to={item.to}
                  aria-label={item.label}
                  onClick={(e) => {
                    if (!confirmNav()) e.preventDefault();
                  }}
                  className={cn(
                    "grid size-11 place-items-center",
                    isPathActive(pathname, item) ? "text-accent" : "text-mut",
                  )}
                >
                  <Icon className="size-[18px]" />
                </Link>
              );
            })}
          </nav>
          <div className="flex items-center gap-2 font-mono text-[9.5px] uppercase tracking-wider text-mut">
            <ClaudeBot active={liveSessions > 0} size={25} />
            {liveSessions ? `${liveSessions} live` : "idle"}
          </div>
        </header>

        <div className="sticky top-0 z-20 flex min-h-11 items-center border-b border-line bg-bg/95 px-4 backdrop-blur lg:px-6">
          <div className="min-w-0 truncate font-mono text-[10px] uppercase tracking-[0.1em]">
            <Crumbs pathname={pathname} />
          </div>
          <div className="ml-auto flex items-center">
            <div className="mr-1 hidden items-center gap-2 font-mono text-[9.5px] uppercase tracking-wider text-dim sm:flex">
              <Activity className="size-3.5" /> Safe control plane
            </div>
            <PresentationToggle />
          </div>
        </div>

        <main className="mx-auto w-full max-w-[1240px] px-4 pb-[calc(88px+env(safe-area-inset-bottom))] pt-4 lg:px-6 lg:pb-10 lg:pt-5">
          <IncidentStrip />
          <Outlet />
        </main>

        <nav
          className="fixed inset-x-0 bottom-0 z-30 flex border-t border-line bg-bg2/95 pb-[env(safe-area-inset-bottom)] backdrop-blur lg:hidden"
          aria-label="Primary navigation"
        >
          {primary.map((item) => navLink(item, true))}
        </nav>
      </div>
    </div>
  );
}
