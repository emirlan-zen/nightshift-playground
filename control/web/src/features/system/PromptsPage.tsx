import { useNavigate, useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { api } from "@/lib/api";
import { qk } from "@/lib/queries";
import { isAgentVisible, useVisibility } from "@/lib/visibility";
import { EmptyText, ErrorText, SectionHeader } from "@/components/SectionHeader";

// A prompt file is a long instruction document; like reports (2.3) it reads far
// better as a full page than a phone modal. /prompts lists; /prompts/:id shows.
export function PromptPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const prompt = useQuery({
    queryKey: ["prompt", id],
    queryFn: () => api.prompt(id!),
    enabled: !!id,
  });
  if (!id) return null;
  return (
    <section>
      <button
        onClick={() => navigate("/prompts")}
        className="mb-4 inline-flex items-center gap-1 font-mono text-[11px] uppercase tracking-wider text-mut transition-colors hover:text-ink"
      >
        <ChevronLeft className="size-3.5" /> Prompts
      </button>
      <h1 className="mb-4 break-all font-head text-[19px] font-bold tracking-tight">{id}</h1>
      {prompt.isLoading && <EmptyText>Loading…</EmptyText>}
      {prompt.error instanceof Error && <ErrorText>{prompt.error.message}</ErrorText>}
      {prompt.data !== undefined && (
        <pre className="m-0 overflow-auto whitespace-pre-wrap break-words border border-line bg-bg p-3.5 font-mono text-[12px] leading-relaxed text-[#c4c8d0]">
          {prompt.data}
        </pre>
      )}
    </section>
  );
}

export function Prompts() {
  const { data, error } = useQuery({ queryKey: qk.prompts, queryFn: api.prompts });
  const navigate = useNavigate();
  const vis = useVisibility();
  // Per-agent prompt groups are titled with the agent name; presentation mode
  // drops the private ones (shared/skills/node groups are never in the hidden
  // list, so they always pass).
  const groups = (data?.groups ?? []).filter((g) => isAgentVisible(g.title, vis));

  return (
    <section>
      {groups.map((g) => (
        <div key={g.title} className="mb-1">
          <SectionHeader className="mb-3 mt-6 first:mt-2" title={g.title} />
          {(g.files ?? []).length ? (
            (g.files ?? []).map((f) => (
              <button
                key={f.id}
                disabled={!f.exists}
                onClick={() =>
                  f.exists &&
                  navigate(
                    f.editable
                      ? `/automations/nodes/${encodeURIComponent(f.id.replace(/^node-/, ""))}`
                      : `/prompts/${encodeURIComponent(f.id)}`,
                  )
                }
                className="-mt-px block w-full border border-line bg-surface px-4 py-3.5 text-left transition-colors enabled:hover:border-line2 enabled:hover:bg-raise disabled:opacity-45"
              >
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <div className="break-words text-[14.5px] font-semibold tracking-tight">
                      {f.label}
                    </div>
                    {f.desc && <div className="mt-1 text-[12px] text-mut">{f.desc}</div>}
                    {f.editable && (
                      <div className="mt-1 font-mono text-[9.5px] uppercase text-accent">
                        {f.source} · revision {f.revision}
                      </div>
                    )}
                    <div className="mt-[5px] break-all font-mono text-[11px] text-dim">
                      {f.path}
                    </div>
                  </div>
                  <span className="flex-none font-mono text-[10px] uppercase tracking-wider text-mut">
                    {f.exists ? (f.editable ? "edit →" : "view →") : "missing"}
                  </span>
                </div>
              </button>
            ))
          ) : (
            <div className="-mt-px border border-line bg-surface px-4 py-3.5 text-[12px] text-dim">
              none
            </div>
          )}
        </div>
      ))}
      <ErrorText>{error instanceof Error ? error.message : null}</ErrorText>
      <div className="mt-[30px] max-w-[64ch] text-[11px] leading-relaxed text-dim">
        Read-only view of the instruction files each agent loads.
      </div>
    </section>
  );
}
