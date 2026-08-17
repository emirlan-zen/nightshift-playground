// Time-oriented grouping for the Night tab (per-night sections + wave timeline).
// Pure functions over the existing API shapes — no new endpoints, no React — so
// they're unit-tested in night.test.ts.

import type { Job, Report, PipelineWave, Flow } from "./api";
import { nightKey } from "./format";

// A run's glanceable state, unifying the three signals the UI already has:
// the job's scheduled/started flags, whether its report landed, and the obs
// ledger status (which alone distinguishes auth-skip vs no-deliverable).
export type RunStatus =
  | "scheduled"
  | "waiting"
  | "running"
  | "delivered"
  | "no-report"
  | "dep-skip"
  | "auth-skip"
  | "done";

/** Status of one job. hasReport wins over a lagging obs ingest; obs is the only
 *  source that can tell auth-skip from no-deliverable. A gated job (ADR-0014) is
 *  "waiting" on its upstreams; a dep-skipped one is a loud terminal failure. */
export function jobStatus(job: Job, hasReport: boolean, obs?: string): RunStatus {
  if (job.skipped) return "dep-skip";
  if (!job.started) return job.gated ? "waiting" : "scheduled";
  if (job.runState === "active") return "running";
  if (hasReport || obs === "delivered") return "delivered";
  if (obs === "auth-skip") return "auth-skip";
  if (obs === "no-deliverable") return "no-report";
  return "done";
}

// Order a wave's aggregate status by how much it wants the operator's eye:
// still-going and not-yet-fired rank above failures, which rank above the
// happy path. Empty (no run carried this wave tonight) is "idle".
const WAVE_PRIORITY: RunStatus[] = [
  "running",
  "dep-skip",
  "no-report",
  "waiting",
  "scheduled",
  "delivered",
  "auth-skip",
  "done",
];

export function aggregateStatus(statuses: RunStatus[]): RunStatus | "idle" {
  if (statuses.length === 0) return "idle";
  for (const s of WAVE_PRIORITY) if (statuses.includes(s)) return s;
  return "done";
}

/** Jobs that carried a given wave this night. A wave `name` equals the job
 *  `label`; exec fans out to "<name> · <lane> · …", so match the prefix too. */
export function jobsForWave(wave: PipelineWave, jobs: Job[]): Job[] {
  return jobs.filter((j) => {
    const l = j.label ?? "";
    return l === wave.name || l.startsWith(wave.name + " ");
  });
}

/** Group items by their night (evening-anchored, see nightKey). Insertion order
 *  within a night is preserved. */
function groupByNight<T>(items: T[], at: (t: T) => string | number): Map<string, T[]> {
  const m = new Map<string, T[]>();
  for (const it of items) {
    const k = nightKey(at(it));
    const bucket = m.get(k);
    if (bucket) bucket.push(it);
    else m.set(k, [it]);
  }
  return m;
}

export interface NightGroup {
  key: string; // "YYYY-MM-DD" of the evening
  jobs: Job[];
  reports: Report[];
}

/** What a flow report row shows beyond its banner frontmatter: which run it
 *  belongs to (goal + template) and the node's own verdict. Built from
 *  /api/flows so a night-history report row can name its flow instead of the
 *  bare "Flow · <date>" run-id fallback (audit P1-3). */
export interface FlowReportMeta {
  goal: string;
  template: string;
  node: string; // the node's id (e.g. "implement-2") — the precise role label,
  // authoritative even when a report carries no banner_wave and distinct across
  // parallel members (role collapses them, node.id does not).
  verdict?: string;
}

/** Index every delivered flow node's report id -> its owning run. A node's
 *  reportId equals its job id, which is exactly the report file stem the night
 *  history lists as Report.id, so this is a direct join with no extra fetch. */
export function indexFlowReports(flows: Flow[]): Map<string, FlowReportMeta> {
  const m = new Map<string, FlowReportMeta>();
  for (const f of flows) {
    for (const n of f.nodeViews ?? []) {
      if (!n.reportId) continue;
      m.set(n.reportId, {
        goal: f.goal,
        template: f.template,
        node: n.id,
        verdict: n.verdict || undefined,
      });
    }
  }
  return m;
}

/** Merge an agent's jobs + reports into per-night groups, newest night first. */
export function nightsFor(jobs: Job[], reports: Report[]): NightGroup[] {
  const jm = groupByNight(jobs, (j) => j.at);
  const rm = groupByNight(reports, (r) => r.mtime * 1000);
  const keys = new Set<string>([...jm.keys(), ...rm.keys()]);
  return [...keys]
    .sort((a, b) => (a < b ? 1 : -1)) // lexical desc = newest night first
    .map((key) => ({ key, jobs: jm.get(key) ?? [], reports: rm.get(key) ?? [] }));
}
