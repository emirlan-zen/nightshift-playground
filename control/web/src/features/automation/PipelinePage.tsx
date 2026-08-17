import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2, CornerDownRight } from "lucide-react";
import { api, type Profile, type ProfileWave, type ProfileSummary, type Proposal } from "@/lib/api";
import { qk, useProfiles, useProposals, useServers } from "@/lib/queries";
import { setNavGuard, confirmNav } from "@/lib/navguard";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Input } from "@/components/ui/field";
import { Switch } from "@/components/ui/switch";
import { EmptyText, ErrorText, SectionHeader } from "@/components/SectionHeader";
import { cn } from "@/lib/utils";

// Profiles keep their existing editor + execution semantics. Since the 2026-07-12
// UI audit the schedules LIST folds into the Automation Studio (SchedulesSection),
// while the wave editor keeps its own /automations/profiles/:name route.

const EFFORTS = ["", "low", "medium", "high", "xhigh", "max"];

// minutes since the 22:30 evening anchor, so 08:03 sorts after 23:00.
function nightOrder(hhmm: string): number {
  const [h, m] = hhmm.split(":").map(Number);
  return h < 12 ? (h + 24) * 60 + m : h * 60 + m;
}

// A client-side mirror of the server's cycle check so the operator gets an
// instant error before saving. Returns a cycle path or "".
function findCycle(waves: ProfileWave[]): string {
  const edges = new Map(waves.map((w) => [w.name, w.after ?? []]));
  const color = new Map<string, number>(); // 0 white 1 grey 2 black
  const stack: string[] = [];
  const dfs = (n: string): string => {
    color.set(n, 1);
    stack.push(n);
    for (const up of edges.get(n) ?? []) {
      if (color.get(up) === 1) return [...stack, up].join(" → ");
      if (!color.get(up)) {
        const c = dfs(up);
        if (c) return c;
      }
    }
    stack.pop();
    color.set(n, 2);
    return "";
  };
  for (const w of waves)
    if (!color.get(w.name)) {
      const c = dfs(w.name);
      if (c) return c;
    }
  return "";
}

// order waves: timed by night order, triggered just after their latest upstream.
function orderWaves(waves: ProfileWave[]): ProfileWave[] {
  const order = new Map<string, number>();
  for (const w of waves) if (w.time) order.set(w.name, nightOrder(w.time));
  for (let i = 0; i < waves.length; i++) {
    for (const w of waves) {
      if (w.time) continue;
      let mx = 0;
      let known = true;
      for (const up of w.after ?? []) {
        const o = order.get(up);
        if (o === undefined) {
          known = false;
          break;
        }
        mx = Math.max(mx, o);
      }
      if (known) order.set(w.name, mx + 1);
    }
  }
  return [...waves].sort((a, b) => (order.get(a.name) ?? 1e9) - (order.get(b.name) ?? 1e9));
}

// ---- proposal inbox ----------------------------------------------------------

function ProposalsBanner() {
  const qc = useQueryClient();
  const { data } = useProposals();
  const invalidate = () =>
    qc
      .invalidateQueries({ queryKey: qk.proposals })
      .then(() => qc.invalidateQueries({ queryKey: qk.profiles }));
  const apply = useMutation({
    mutationFn: (n: string) => api.applyProposal(n),
    onSettled: invalidate,
  });
  const dismiss = useMutation({
    mutationFn: (n: string) => api.dismissProposal(n),
    onSettled: invalidate,
  });
  const proposals = data ?? [];
  if (proposals.length === 0) return null;
  return (
    <div className="mb-5 border border-review/60 bg-review/5 p-3.5">
      <div className="mb-2 font-mono text-[10.5px] font-bold uppercase tracking-wider text-review">
        retro proposed {proposals.length} profile{proposals.length > 1 ? "s" : ""}
      </div>
      <div className="space-y-2.5">
        {proposals.map((p: Proposal) => (
          <div
            key={p.name}
            className="flex flex-wrap items-start gap-2 border-t border-line pt-2.5 first:border-t-0 first:pt-0"
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="font-head text-[13px] font-bold tracking-tight">{p.name}</span>
                {p.valid ? (
                  <Badge tone="ok">{p.profile?.waves.length ?? 0} waves</Badge>
                ) : (
                  <Badge tone="danger">invalid</Badge>
                )}
              </div>
              {p.why && <div className="mt-1 text-[12px] leading-snug text-mut">{p.why}</div>}
              {p.error && <div className="mt-1 font-mono text-[10.5px] text-danger">{p.error}</div>}
            </div>
            <div className="flex flex-none gap-1.5">
              {p.valid && (
                <Button
                  size="sm"
                  variant="accent"
                  disabled={apply.isPending}
                  onClick={() => apply.mutate(p.name)}
                >
                  Apply
                </Button>
              )}
              <Button size="sm" disabled={dismiss.isPending} onClick={() => dismiss.mutate(p.name)}>
                Dismiss
              </Button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

// ---- profile list ------------------------------------------------------------

function ProfileRow({
  p,
  agent,
  onEdit,
}: {
  p: ProfileSummary;
  agent: string;
  onEdit: () => void;
}) {
  const qc = useQueryClient();
  const invalidate = () => qc.invalidateQueries({ queryKey: qk.profiles });
  const activate = useMutation({
    mutationFn: () => api.activateProfile(p.name, agent),
    onSettled: invalidate,
  });
  const run = useMutation({ mutationFn: () => api.runProfile(p.name, agent) });
  return (
    <div className="flex flex-wrap items-center gap-2 border-t border-line py-3 first:border-t-0">
      <button
        onClick={onEdit}
        className="min-w-0 flex-1 text-left transition-opacity hover:opacity-70"
      >
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-head text-[14px] font-bold tracking-tight">{p.name}</span>
          {p.active && <Badge tone="accent">active</Badge>}
          {p.effective && !p.active && <Badge tone="sched">tonight</Badge>}
          {p.hasFanout && <Badge tone="muted">fan-out</Badge>}
        </div>
        <div className="mt-1 font-mono text-[10px] tracking-wide text-dim">
          {p.waves} waves
          {p.deadline ? ` · deadline ${p.deadline}` : ""}
          {p.workers ? ` · ${p.workers.hunt}h/${p.workers.improve}i` : ""}
          {" · edit →"}
        </div>
      </button>
      <div className="flex flex-none gap-1.5">
        {!p.active && (
          <Button size="sm" disabled={activate.isPending} onClick={() => activate.mutate()}>
            Activate
          </Button>
        )}
        <Button size="sm" variant="accent" disabled={run.isPending} onClick={() => run.mutate()}>
          {run.isPending ? "…" : "Run now"}
        </Button>
      </div>
      {run.isError && (
        <div className="w-full">
          <ErrorText>{run.error instanceof Error ? run.error.message : null}</ErrorText>
        </div>
      )}
      {run.isSuccess && (
        <span className="w-full font-mono text-[10px] uppercase tracking-wider text-ok">
          run minted ✓ — see Runs
        </span>
      )}
    </div>
  );
}

// ---- wave form ---------------------------------------------------------------

function WaveForm({
  wave,
  others,
  onSave,
  onClose,
}: {
  wave: ProfileWave;
  others: ProfileWave[]; // the rest of the profile's waves (for the after picker + cycle check)
  onSave: (w: ProfileWave) => void;
  onClose: () => void;
}) {
  const [w, setW] = useState<ProfileWave>(wave);
  const set = <K extends keyof ProfileWave>(k: K, v: ProfileWave[K]) =>
    setW((p) => ({ ...p, [k]: v }));
  const after = w.after ?? [];
  const toggleAfter = (name: string) =>
    set("after", after.includes(name) ? after.filter((n) => n !== name) : [...after, name]);

  const nameTaken = others.some((o) => o.name === w.name && o !== wave);
  const cycle = findCycle([...others.filter((o) => o.name !== w.name), w]);
  const err = !w.name.trim()
    ? "name is required"
    : nameTaken
      ? "name already used"
      : !w.time && after.length === 0
        ? "a wave needs a time or an after edge"
        : !w.prompt.trim()
          ? "prompt file is required"
          : after.length > 0 && w.perTicket
            ? "after + perTicket unsupported"
            : w.executor === "codex" && w.perTicket
              ? "perTicket fan-out is claude-only"
              : cycle
                ? `cycle: ${cycle}`
                : "";

  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title={`Wave · ${wave.name || "new"}`}>
      <div className="space-y-3.5">
        <Field label="Name">
          <Input
            value={w.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="analyze"
          />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Time (HH:MM, optional if triggered)">
            <Input
              value={w.time ?? ""}
              onChange={(e) => set("time", e.target.value)}
              placeholder="00:00"
            />
          </Field>
          <Field label="Auto-stop minutes">
            <Input
              type="number"
              min={10}
              max={480}
              value={w.minutes || ""}
              onChange={(e) => set("minutes", Number(e.target.value))}
            />
          </Field>
        </div>
        <Field label="Prompt file (~/.nightshift/sweep/…)">
          <Input
            value={w.prompt}
            onChange={(e) => set("prompt", e.target.value)}
            placeholder="playground-exec.md"
          />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Model (optional)">
            <Input
              value={w.model ?? ""}
              onChange={(e) => set("model", e.target.value)}
              placeholder="claude-opus-4-8"
            />
          </Field>
          <Field label="Effort">
            <select
              value={w.effort ?? ""}
              onChange={(e) => set("effort", e.target.value)}
              className="w-full border border-line2 bg-bg px-3 py-2.5 font-mono text-[12px] uppercase tracking-wider text-ink focus:border-accent focus:outline-none"
            >
              {EFFORTS.map((e) => (
                <option key={e} value={e}>
                  {e || "default"}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <Field label="Executor (codex = OpenAI Codex CLI, ADR-0018)">
          <select
            value={w.executor ?? ""}
            onChange={(e) => set("executor", e.target.value || undefined)}
            className="w-full border border-line2 bg-bg px-3 py-2.5 font-mono text-[12px] uppercase tracking-wider text-ink focus:border-accent focus:outline-none"
          >
            <option value="">claude (default)</option>
            <option value="codex">codex</option>
          </select>
        </Field>
        {others.length > 0 && (
          <Field label="After (fan-out: consume these waves' output — leave empty for a scheduled wave)">
            <div className="flex flex-wrap gap-1.5">
              {others
                .filter((o) => o.name !== w.name)
                .map((o) => (
                  <button
                    key={o.name}
                    onClick={() => toggleAfter(o.name)}
                    className={cn(
                      "border px-2.5 py-1.5 font-mono text-[10.5px] tracking-wide transition-colors",
                      after.includes(o.name)
                        ? "border-accent bg-accent text-ink-on-accent"
                        : "border-line2 text-mut hover:border-ink hover:text-ink",
                    )}
                  >
                    {o.name}
                  </button>
                ))}
            </div>
          </Field>
        )}
        <div className="flex gap-5">
          <label className="flex cursor-pointer select-none items-center gap-2 font-mono text-[10.5px] uppercase tracking-wider text-mut">
            <Switch checked={!!w.perTicket} onCheckedChange={(v) => set("perTicket", v)} />
            perTicket (exec fan-out)
          </label>
          <label className="flex cursor-pointer select-none items-center gap-2 font-mono text-[10.5px] uppercase tracking-wider text-mut">
            <Switch checked={!!w.noTickets} onCheckedChange={(v) => set("noTickets", v)} />
            noTickets
          </label>
        </div>
        {err && <div className="font-mono text-[11px] text-danger">{err}</div>}
        <div className="flex gap-2.5 pt-1">
          <Button
            variant="accent"
            size="md"
            className="flex-1"
            disabled={!!err}
            onClick={() => onSave(w)}
          >
            Save wave
          </Button>
          <Button size="md" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block">
      <div className="mb-1.5 font-mono text-[10px] uppercase tracking-wider text-mut">{label}</div>
      {children}
    </label>
  );
}

// ---- profile editor ----------------------------------------------------------

// ProfileEditor loads the profile (or seeds a new one from the built-in) and
// hands a stable `initial` to EditorInner, which is keyed by name so its draft
// state resets on navigation — no setState-in-effect.
function ProfileEditor({ name }: { name: string }) {
  const isNew = name === "new";
  const { data: doc } = useProfiles();
  const q = useQuery({
    queryKey: ["profile", name],
    queryFn: () => api.profile(name),
    enabled: !isNew,
  });

  if (q.error instanceof Error) return <ErrorText>{q.error.message}</ErrorText>;
  const initial: Profile | null = isNew
    ? doc
      ? { ...doc.builtin, name: "" }
      : null
    : (q.data ?? null);
  if (!initial) return <EmptyText>Loading profile…</EmptyText>;
  return <EditorInner key={name} name={name} isNew={isNew} initial={initial} />;
}

function EditorInner({ name, isNew, initial }: { name: string; isNew: boolean; initial: Profile }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  // The schedules list lives in the Automation Studio now (ADR-0020); the editor
  // returns there on save / cancel / delete.
  const basePath = "/automations";
  const [draft, setDraft] = useState<Profile>(initial);
  const [editing, setEditing] = useState<ProfileWave | null>(null);

  const original = useMemo(() => JSON.stringify(initial), [initial]);
  const dirty = JSON.stringify(draft) !== original;

  useEffect(() => {
    setNavGuard(dirty ? "Unsaved changes to this profile — discard them?" : null);
    return () => setNavGuard(null);
  }, [dirty]);

  const save = useMutation({
    mutationFn: () => api.saveProfile(draft.name, draft),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.profiles });
      qc.invalidateQueries({ queryKey: qk.pipeline });
      navigate(basePath);
    },
  });
  const del = useMutation({
    mutationFn: () => api.deleteProfile(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.profiles });
      navigate(basePath);
    },
  });

  const waves = orderWaves(draft.waves);
  const upsertWave = (w: ProfileWave) => {
    setDraft((d) => {
      const idx = d.waves.findIndex((x) => x.name === editing?.name);
      const next = [...d.waves];
      if (idx >= 0) next[idx] = w;
      else next.push(w);
      return { ...d, waves: next };
    });
    setEditing(null);
  };
  const removeWave = (n: string) =>
    setDraft((d) => ({ ...d, waves: d.waves.filter((x) => x.name !== n) }));

  return (
    <section>
      <SectionHeader
        className="mb-4"
        title={isNew ? "New profile" : draft.name}
        action={
          <Button size="sm" onClick={() => confirmNav() && navigate(basePath)}>
            ← Studio
          </Button>
        }
      />

      {/* profile-level fields */}
      <Card className="mb-3 p-4">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Field label="Profile name">
            <Input
              value={draft.name}
              disabled={!isNew}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
              placeholder="deep-perf"
            />
          </Field>
          <Field label="Deadline (HH:MM, optional)">
            <Input
              value={draft.deadline ?? ""}
              onChange={(e) => setDraft({ ...draft, deadline: e.target.value })}
              placeholder="08:45"
            />
          </Field>
          <Field label="Exec workers (hunt / improve, ≤6)">
            <div className="flex gap-2">
              <Input
                type="number"
                min={0}
                max={6}
                value={draft.workers?.hunt ?? 3}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    workers: { hunt: Number(e.target.value), improve: draft.workers?.improve ?? 3 },
                  })
                }
              />
              <Input
                type="number"
                min={0}
                max={6}
                value={draft.workers?.improve ?? 3}
                onChange={(e) =>
                  setDraft({
                    ...draft,
                    workers: { hunt: draft.workers?.hunt ?? 3, improve: Number(e.target.value) },
                  })
                }
              />
            </div>
          </Field>
        </div>
      </Card>

      {/* wave list */}
      <Card className="mb-3 p-4">
        <div className="mb-2 flex items-center justify-between">
          <span className="font-mono text-[10px] font-bold uppercase tracking-[0.12em] text-mut">
            Waves
          </span>
          <Button size="sm" onClick={() => setEditing({ name: "", prompt: "", minutes: 60 })}>
            <Plus className="size-3" /> Wave
          </Button>
        </div>
        {waves.length === 0 ? (
          <EmptyText>No waves yet.</EmptyText>
        ) : (
          waves.map((w) => (
            <div
              key={w.name}
              className="flex items-center gap-2 border-t border-line py-2.5 first:border-t-0"
            >
              <button
                onClick={() => setEditing(w)}
                className="flex min-w-0 flex-1 items-center gap-2 text-left transition-opacity hover:opacity-70"
              >
                {(w.after?.length ?? 0) > 0 && (
                  <CornerDownRight className="size-3.5 shrink-0 text-dim" />
                )}
                <span className="font-mono text-[11px] text-mut">{w.time || "▸"}</span>
                <span className="min-w-0 truncate text-[13px] font-semibold tracking-tight">
                  {w.name}
                </span>
                {w.perTicket && <Badge tone="muted">fan-out</Badge>}
                {(w.after?.length ?? 0) > 0 && (
                  <span className="truncate font-mono text-[10px] text-dim">
                    after {w.after!.join(", ")}
                  </span>
                )}
              </button>
              <button
                aria-label={`Delete ${w.name}`}
                onClick={() => removeWave(w.name)}
                className="grid size-7 shrink-0 place-items-center border border-line2 text-mut transition-colors hover:border-danger hover:text-danger"
              >
                <Trash2 className="size-3.5" />
              </button>
            </div>
          ))
        )}
      </Card>

      <div className="flex items-center gap-2.5">
        <Button
          variant="accent"
          size="md"
          className="flex-1 sm:flex-none sm:px-8"
          disabled={!draft.name.trim() || draft.waves.length === 0 || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save profile"}
        </Button>
        {!isNew && (
          <Button size="md" onClick={() => del.mutate()} disabled={del.isPending}>
            Delete
          </Button>
        )}
      </div>
      <ErrorText>{save.error instanceof Error ? save.error.message : null}</ErrorText>
      <ErrorText>{del.error instanceof Error ? del.error.message : null}</ErrorText>

      {editing && (
        <WaveForm
          wave={editing}
          others={draft.waves}
          onSave={upsertWave}
          onClose={() => setEditing(null)}
        />
      )}
    </section>
  );
}

// ---- top-level ---------------------------------------------------------------

// The profile editor keeps its own sub-route (/automations/profiles/:name); the
// bare index route redirects to the Automation Studio, which renders the list
// via SchedulesSection below.
export function Pipeline() {
  const { name } = useParams();
  return <ProfileEditor name={name ?? "new"} />;
}

// The schedules list, folded into the Automation Studio (ADR-0020): recurring
// profiles, on-demand "Run now", and retro's profile proposals — one section of
// /automations, no separate /pipeline page.
export function SchedulesSection() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const agent = params.get("c") || "playground";
  const { data: servers } = useServers();
  const agents = (servers ?? []).map((s) => s.company);
  const { data, error, isLoading } = useProfiles(agent);
  const profiles = data?.profiles ?? [];

  return (
    <div>
      <ProposalsBanner />
      <SectionHeader
        className="mb-2"
        title="Schedules"
        meta="recurring profiles + on-demand"
        action={
          <div className="flex items-center gap-2">
            {agents.length > 1 && (
              <select
                aria-label="Agent"
                value={agent}
                onChange={(e) =>
                  setParams(e.target.value === "playground" ? {} : { c: e.target.value })
                }
                className="border border-line2 bg-bg px-2 py-1.5 font-mono text-[10px] uppercase tracking-wider text-ink focus:border-accent focus:outline-none"
              >
                {agents.map((a) => (
                  <option key={a} value={a}>
                    {a}
                  </option>
                ))}
              </select>
            )}
            <Button size="sm" onClick={() => navigate("/automations/profiles/new")}>
              <Plus className="size-3" /> New
            </Button>
          </div>
        }
      />
      <div className="mb-2 font-mono text-[10px] uppercase tracking-wider text-dim">
        active for <span className="text-mut">{agent}</span>:{" "}
        <span className="text-ink">{data?.active || "built-in default"}</span>
      </div>
      {isLoading ? (
        <EmptyText>Loading schedules…</EmptyText>
      ) : error instanceof Error ? (
        <ErrorText>{error.message}</ErrorText>
      ) : (
        <Card className="px-4 py-2">
          {profiles.length === 0 ? (
            <div className="py-3">
              <EmptyText>
                No profiles — the built-in default pipeline runs. Create one to switch the night's
                shape.
              </EmptyText>
            </div>
          ) : (
            profiles.map((p) => (
              <ProfileRow
                key={p.name}
                p={p}
                agent={agent}
                onEdit={() => navigate(`/automations/profiles/${encodeURIComponent(p.name)}`)}
              />
            ))
          )}
        </Card>
      )}
      <p className="mt-3 max-w-[64ch] text-[11px] leading-relaxed text-dim">
        Exactly one profile is active. <b>Activate</b> takes effect the next night — a mid-night
        switch never half-changes the running night. <b>Run now</b> starts a profile's waves offset
        from now (a daytime/away pass). Retro may <i>propose</i> a profile; only your tap applies
        it.
      </p>
    </div>
  );
}
