import { useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "./api";
import type {
  Flow,
  Health,
  LedgerRun,
  Obs,
  RepoOption,
  ServerState,
  TicketGroup,
  Usage,
} from "./api";
import { isAgentVisible, useVisibility, type VisibilityState } from "./visibility";

// Presentation mode is enforced HERE, at the one choke point every view reads
// through: hooks whose payloads are agent-scoped drop hidden agents via a
// react-query `select`, so pages, nav counts, and agent pickers all follow
// without knowing the feature exists. The cache keeps the unfiltered payload —
// toggling the eye never refetches, and System's visibility card can read the
// raw list through the same key.
function useVisibilitySelect<T>(filter: (data: T, vis: VisibilityState) => T) {
  const vis = useVisibility();
  return useCallback((data: T) => filter(data, vis), [filter, vis]);
}

const filterServers = (data: ServerState[], vis: VisibilityState) =>
  data.filter((s) => isAgentVisible(s.company, vis));
const filterFlows = (data: Flow[], vis: VisibilityState) =>
  data.filter((f) => isAgentVisible(f.agent, vis));
const filterTickets = (data: TicketGroup[], vis: VisibilityState) =>
  data.filter((g) => isAgentVisible(g.agent, vis));
const filterLedger = (data: LedgerRun[], vis: VisibilityState) =>
  data.filter((r) => isAgentVisible(r.agent, vis));
const filterRepos = (data: RepoOption[], vis: VisibilityState) =>
  data.filter((r) => isAgentVisible(r.agent, vis));
const filterObs = (data: Obs, vis: VisibilityState): Obs => ({
  ...data,
  alerts: data.alerts.filter((a) => isAgentVisible(a.agent, vis)),
  agents: data.agents.filter((a) => isAgentVisible(a.agent, vis)),
  runs: data.runs.filter((r) => isAgentVisible(r.agent, vis)),
});
const filterHealth = (data: Health, vis: VisibilityState): Health => ({
  ...data,
  recentFailures: data.recentFailures.filter((f) => isAgentVisible(f.agent, vis)),
  forge: data.forge?.filter((f) => isAgentVisible(f.company, vis)),
  depSkips: data.depSkips?.filter((s) => isAgentVisible(s.agent, vis)),
});
const filterUsage = (data: Usage, vis: VisibilityState): Usage => ({
  ...data,
  agents: data.agents.filter((a) => isAgentVisible(a.agent, vis)),
});

export const qk = {
  status: ["status"] as const,
  sweeps: ["sweeps"] as const,
  jobs: (c: string) => ["jobs", c] as const,
  reports: (c: string) => ["reports", c] as const,
  pipeline: ["pipeline"] as const,
  profiles: ["profiles"] as const,
  proposals: ["proposals"] as const,
  tickets: ["tickets"] as const,
  prompts: ["prompts"] as const,
  prompt: (id: string) => ["prompt-document", id] as const,
  focus: ["focus"] as const,
  ideas: ["ideas"] as const,
  idea: (id: string) => ["idea", id] as const,
  usage: ["usage"] as const,
  health: ["health"] as const,
  obs: ["obs"] as const,
  vitals: ["vitals"] as const,
  flows: ["flows"] as const,
  flowCatalog: ["flow-catalog"] as const,
  repos: ["repos"] as const,
  ledger: ["ledger"] as const,
  changeProposals: ["change-proposals"] as const,
  runScores: (id: string) => ["scores", "run", id] as const,
  automationScores: (id: string) => ["scores", "automation", id] as const,
};

export function useServers(refetchInterval: number | false = 6000) {
  return useQuery({
    queryKey: qk.status,
    queryFn: api.status,
    refetchInterval,
    select: useVisibilitySelect(filterServers),
  });
}

// The unfiltered server list — same cache entry as useServers, no extra
// polling. Only for surfaces that manage presentation mode itself (System's
// visibility card must list hidden agents to let the operator unhide them).
export function useServersUnfiltered() {
  return useQuery({ queryKey: qk.status, queryFn: api.status });
}

export function useFlows(refetchInterval: number | false = 8000) {
  return useQuery({
    queryKey: qk.flows,
    queryFn: api.flows,
    refetchInterval,
    select: useVisibilitySelect(filterFlows),
  });
}

export function useFlowCatalog() {
  return useQuery({ queryKey: qk.flowCatalog, queryFn: api.flowCatalog, staleTime: 5 * 60_000 });
}

export function usePromptDocument(id: string) {
  return useQuery({
    queryKey: qk.prompt(id),
    queryFn: () => api.promptDocument(id),
    enabled: !!id,
  });
}

export function useRepos() {
  return useQuery({
    queryKey: qk.repos,
    queryFn: api.repos,
    staleTime: 5 * 60_000,
    select: useVisibilitySelect(filterRepos),
  });
}

// Night-run jobs for one agent. Shared by the Night tab (schedule + reports) and
// the Servers tab (live-session visibility), so both poll one cache.
export function useJobs(agent: string, refetchInterval: number | false = 10000) {
  return useQuery({
    queryKey: qk.jobs(agent),
    queryFn: () => api.jobs(agent),
    refetchInterval,
  });
}

export function useTickets() {
  return useQuery({
    queryKey: qk.tickets,
    queryFn: api.tickets,
    select: useVisibilitySelect(filterTickets),
  });
}

// playground's wave schedule — the built-in pipeline or the pipeline.json
// override. Rarely changes, so cache it long and don't poll.
export function usePipeline(agent?: string) {
  return useQuery({
    queryKey: [...qk.pipeline, agent ?? "playground"],
    queryFn: () => api.pipeline(agent),
    staleTime: 5 * 60_000,
  });
}

// Pipeline profiles (ADR-0014): the night's switchable shapes + retro's
// proposal inbox. Change rarely; cache long, don't poll.
export function useProfiles(agent?: string) {
  return useQuery({
    queryKey: [...qk.profiles, agent ?? "playground"],
    queryFn: () => api.profiles(agent),
    staleTime: 60_000,
  });
}
export function useProposals() {
  return useQuery({ queryKey: qk.proposals, queryFn: api.proposals, staleTime: 60_000 });
}

// ADR-0017: the run outcome ledger (metadata-only) and the widened definition
// proposal inbox (node prompts / node defs / templates).
export function useLedger() {
  return useQuery({
    queryKey: qk.ledger,
    queryFn: api.ledger,
    staleTime: 60_000,
    select: useVisibilitySelect(filterLedger),
  });
}
// ADR-0023: judge scores. A run's scores land WHILE it runs, so a live run must
// actually poll — staleTime alone left an already-open run page showing nothing
// until the operator refocused the tab. A terminal run's scores never change.
export function useRunScores(id: string, live = false, enabled = true) {
  return useQuery({
    queryKey: qk.runScores(id),
    queryFn: () => api.runScores(id),
    enabled: enabled && Boolean(id),
    staleTime: 20_000,
    refetchInterval: live ? 20_000 : false,
  });
}
export function useAutomationScores(id: string, enabled = true) {
  return useQuery({
    queryKey: qk.automationScores(id),
    queryFn: () => api.automationScores(id),
    enabled: enabled && Boolean(id),
    staleTime: 60_000,
  });
}
export function useChangeProposals() {
  return useQuery({
    queryKey: qk.changeProposals,
    queryFn: api.changeProposals,
    staleTime: 60_000,
  });
}

export function useObs(refetchInterval: number | false = 30000) {
  return useQuery({
    queryKey: qk.obs,
    queryFn: api.obs,
    refetchInterval,
    select: useVisibilitySelect(filterObs),
  });
}

// Claude auth health — the night pipeline is dead if this is red (ADR-0006).
// Refetched on a slow cadence; the server itself only re-probes every 10 min.
export function useHealth(refetchInterval: number | false = 60000) {
  return useQuery({
    queryKey: qk.health,
    queryFn: api.health,
    refetchInterval,
    select: useVisibilitySelect(filterHealth),
  });
}

// Usage rollup. The per-agent rows follow presentation mode; the totals /
// per-day / per-model tables stay as-is (they carry no agent names).
export function useUsage(refetchInterval: number | false = 60000) {
  return useQuery({
    queryKey: qk.usage,
    queryFn: api.usage,
    refetchInterval,
    select: useVisibilitySelect(filterUsage),
  });
}

// Box vitals (load/mem/disk/uptime). Server caches ~4s; a 15s poll keeps the
// card live without hammering /proc.
export function useVitals(refetchInterval: number | false = 15000) {
  return useQuery({
    queryKey: qk.vitals,
    queryFn: api.vitals,
    refetchInterval,
  });
}
