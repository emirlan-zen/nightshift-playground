import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type FocusFile, type Idea } from "@/lib/api";
import { qk } from "@/lib/queries";
import { setNavGuard, confirmNav } from "@/lib/navguard";
import { fmtAt } from "@/lib/format";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/field";
import { Kicker } from "@/components/ui/card";
import { Markdown } from "@/components/Markdown";
import { EmptyText, ErrorText, SectionHeader } from "@/components/SectionHeader";
import { cn } from "@/lib/utils";

// Focus tab: phone-edit the two operator north-star files (ADR-0008).
// products.md gates Lane A; projects.md carries the curated repos + per-project
// autonomy. Deliberately a plain monospace textarea — the steering wheel, not
// an IDE. Selected file lives in the URL (/focus/products).

const LABELS: Record<string, string> = {
  products: "Promoted bets — the gate on Lane A (product hunting).",
  projects:
    "Curated repos + per-project autonomy (full-auto | merge-to-main | pr-only | plan-only).",
};

function Editor({ file }: { file: FocusFile }) {
  const qc = useQueryClient();
  const [text, setText] = useState(file.content);
  // `base` is the server content this editor is diffed against. When the file's
  // content changes upstream — the case that matters is a Promote appending a
  // block to products.md, which invalidates qk.focus and refetches — adopt it
  // *iff* the editor has no unsaved edits, so the textarea and the Save button
  // reflect the real document. Without this, `text` (seeded once) stays stale,
  // the editor looks dirty, and a Save overwrites the appended block with the
  // pre-promotion text. If the user *does* have unsaved edits we keep them: a
  // genuine conflict is theirs to resolve, not ours to silently discard. This
  // is React's supported "adjust state during render" pattern — it re-renders
  // immediately, before paint, so no stale frame is shown.
  const [base, setBase] = useState(file.content);
  if (file.content !== base) {
    if (text === base) setText(file.content);
    setBase(file.content);
  }
  const dirty = text !== file.content;

  const save = useMutation({
    mutationFn: () => api.saveFocus(file.id, text),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.focus }),
  });

  // Dirty guard: in-app tab/file switches ask via the nav guard; a hard
  // reload/close gets the browser's native beforeunload prompt.
  useEffect(() => {
    setNavGuard(dirty ? `Unsaved changes to ${file.id}.md — discard them?` : null);
    return () => setNavGuard(null);
  }, [dirty, file.id]);
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  return (
    <div>
      <div className="mb-2.5 flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <span className="font-mono text-[11px] text-mut">~/.nightshift/focus/{file.id}.md</span>
        <span className="font-mono text-[10px] text-dim">
          {file.modifiedAt > 0 ? `updated ${fmtAt(file.modifiedAt * 1000)}` : "not created yet"}
        </span>
      </div>
      <Textarea
        aria-label={`${file.id}.md content`}
        value={text}
        onChange={(e) => setText(e.target.value)}
        spellCheck={false}
        className="min-h-[52dvh] text-[13px] leading-relaxed"
      />
      <div className="mt-2.5 flex items-center gap-3">
        <Button
          variant="accent"
          size="md"
          className="flex-1 sm:flex-none sm:px-8"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </Button>
        {dirty && !save.isPending && (
          <span className="font-mono text-[10.5px] uppercase tracking-wider text-review">
            unsaved changes
          </span>
        )}
        {!dirty && save.isSuccess && (
          <span className="font-mono text-[10.5px] uppercase tracking-wider text-ok">saved ✓</span>
        )}
      </div>
      <ErrorText>{save.error instanceof Error ? save.error.message : null}</ErrorText>
    </div>
  );
}

// IdeaRow: one backlog entry. Collapsed shows title + date + Promote; expanding
// lazy-loads the full markdown body and reveals an optional note + a one-tap
// promote into products.md (the append the operator would otherwise SSH in).
function IdeaRow({ idea }: { idea: Idea }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [note, setNote] = useState("");

  const body = useQuery({
    queryKey: qk.idea(idea.id),
    queryFn: () => api.idea(idea.id),
    enabled: open,
  });

  const promote = useMutation({
    mutationFn: () => api.promoteIdea(idea.id, note),
    onSuccess: () => {
      // products.md gained a block; refresh the editor + close the row.
      qc.invalidateQueries({ queryKey: qk.focus });
      setOpen(false);
      setNote("");
    },
  });

  return (
    <div className="border border-line2">
      <button
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-baseline justify-between gap-3 px-3 py-2.5 text-left hover:bg-line2/20"
      >
        <span className="text-[13px] text-ink">{idea.title}</span>
        <span className="shrink-0 font-mono text-[10px] text-dim">
          {idea.modifiedAt > 0 ? fmtAt(idea.modifiedAt * 1000) : idea.id}
        </span>
      </button>
      {open && (
        <div className="border-t border-line2 px-3 py-3">
          {body.isLoading && <EmptyText>Loading idea…</EmptyText>}
          {body.error instanceof Error && <ErrorText>{body.error.message}</ErrorText>}
          {body.data && (
            <Markdown
              source={body.data.content}
              className="mb-3 max-h-[40dvh] overflow-y-auto text-[12px]"
            />
          )}
          <Textarea
            aria-label={`promotion note for ${idea.id}`}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="Optional note — why this bet, what to validate first…"
            spellCheck={false}
            className="mb-2.5 min-h-[64px] text-[12px]"
          />
          <div className="flex items-center gap-3">
            <Button
              variant="accent"
              size="md"
              disabled={promote.isPending}
              onClick={() => promote.mutate()}
            >
              {promote.isPending ? "Promoting…" : "Promote → products.md"}
            </Button>
            <span className="font-mono text-[10px] text-dim">appends to the Lane A gate</span>
          </div>
          <ErrorText>{promote.error instanceof Error ? promote.error.message : null}</ErrorText>
        </div>
      )}
    </div>
  );
}

function IdeasPanel() {
  const { data, error, isLoading } = useQuery({ queryKey: qk.ideas, queryFn: api.ideas });
  const ideas = data?.files ?? [];
  return (
    <div className="mt-10">
      <SectionHeader className="mb-3" title="Ideas backlog" />
      <div className="mb-3 max-w-[64ch] text-[12px] leading-relaxed text-mut">
        Scout writes discovery ideas here nightly; promoting one appends it to products.md so the
        next plan-products wave picks it up. Nothing auto-builds — promotion is the gate.
      </div>
      {isLoading && <EmptyText>Loading ideas…</EmptyText>}
      {error instanceof Error && <ErrorText>{error.message}</ErrorText>}
      {!isLoading && !error && ideas.length === 0 && (
        <EmptyText>No ideas yet — the scout wave writes them overnight.</EmptyText>
      )}
      <div className="flex flex-col gap-1.5">
        {ideas.map((idea) => (
          <IdeaRow key={idea.id} idea={idea} />
        ))}
      </div>
    </div>
  );
}

export function Focus() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { data, error, isLoading } = useQuery({ queryKey: qk.focus, queryFn: api.focus });

  const files = data?.files ?? [];
  const selId = id && files.some((f) => f.id === id) ? id : files[0]?.id;
  const file = files.find((f) => f.id === selId);

  if (isLoading) return <EmptyText>Loading focus files…</EmptyText>;
  if (error instanceof Error) return <ErrorText>{error.message}</ErrorText>;

  return (
    <section>
      <div className="mb-4">
        <Kicker>// operator north stars · read by the planners at mint</Kicker>
      </div>

      <SectionHeader
        className="mb-4"
        title="Focus"
        action={
          <div className="flex gap-0.5">
            {files.map((f) => {
              const active = f.id === selId;
              return (
                <button
                  key={f.id}
                  onClick={() => {
                    if (f.id === selId || !confirmNav()) return;
                    navigate(`/focus/${f.id}`);
                  }}
                  className={cn(
                    "inline-flex min-h-10 items-center border px-3.5 font-mono text-[10.5px] font-bold uppercase tracking-wider transition-colors",
                    active
                      ? "border-accent bg-accent text-ink-on-accent"
                      : "border-line2 text-mut hover:border-ink hover:text-ink",
                  )}
                >
                  {f.id}
                </button>
              );
            })}
          </div>
        }
      />

      {file ? (
        <>
          <div className="mb-3 max-w-[64ch] text-[12px] leading-relaxed text-mut">
            {LABELS[file.id]}
          </div>
          <Editor key={file.id} file={file} />
        </>
      ) : (
        <EmptyText>No focus files.</EmptyText>
      )}

      <div className="mt-8 max-w-[64ch] text-[11px] leading-relaxed text-dim">
        These two files steer the night pipeline: scout writes ideas, but only what you promote into
        products.md gets validation tickets; projects.md decides which repos get deep nightly work
        and how far each may go on its own. Saved atomically — a wave never reads a torn file.
      </div>

      <IdeasPanel />
    </section>
  );
}
