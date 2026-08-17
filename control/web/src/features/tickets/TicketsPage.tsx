import { useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { api, type Ticket, type TicketStatus, type TicketLane } from "@/lib/api";
import { qk, useTickets } from "@/lib/queries";
import { fmtAt, fmtRunId } from "@/lib/format";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Textarea } from "@/components/ui/field";
import { Modal } from "@/components/ui/modal";
import { Markdown } from "@/components/Markdown";
import { ErrorText, SectionHeader } from "@/components/SectionHeader";
import { cn } from "@/lib/utils";

const STATUSES: TicketStatus[] = ["open", "review", "closed"];

function statusBadge(st: TicketStatus) {
  return st === "open" ? (
    <Badge tone="accent">open</Badge>
  ) : st === "review" ? (
    <Badge tone="review">review</Badge>
  ) : (
    <Badge tone="done">closed</Badge>
  );
}

function TicketCard({ t, onOpen }: { t: Ticket; onOpen: () => void }) {
  return (
    <button
      draggable
      onClick={onOpen}
      onDragStart={(e) => e.dataTransfer.setData("text/plain", `${t.agent}|${t.id}`)}
      className={cn(
        "mb-2 block w-full border border-line2 border-l-2 bg-surface p-3 text-left transition-colors last:mb-0 hover:bg-raise",
        t.status === "open" && "border-l-accent",
        t.status === "review" && "border-l-review",
        t.status === "closed" && "border-l-line2",
      )}
    >
      <div className="break-words text-[13px] font-semibold leading-snug tracking-tight">
        {t.title}
      </div>
      <div className="mt-[7px] flex flex-wrap items-center gap-x-2.5 gap-y-1 font-mono text-[10px] text-dim">
        <span>{t.createdBy}</span>
        <span>{fmtAt(t.created)}</span>
        {!!t.notes?.length && <span>✎ {t.notes.length}</span>}
        {t.lane && (
          <span className="border border-line2 px-1 py-px uppercase tracking-wider text-mut">
            {t.lane}
          </span>
        )}
        {t.claim && (
          <span className="border border-accent/50 px-1 py-px uppercase tracking-wider text-accent">
            claimed · {t.claim}
          </span>
        )}
      </div>
    </button>
  );
}

function AgentBoard({ agent, tickets }: { agent: string; tickets: Ticket[] }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const boardRef = useRef<HTMLDivElement>(null);
  const [composing, setComposing] = useState(false);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [lane, setLane] = useState<TicketLane>("");
  const [dropCol, setDropCol] = useState<string | null>(null);

  const open = tickets.filter((t) => t.status === "open").length;

  const create = useMutation({
    mutationFn: () => api.createTicket(agent, title.trim(), body.trim(), lane),
    onSuccess: () => {
      setTitle("");
      setBody("");
      setLane("");
      setComposing(false);
      qc.invalidateQueries({ queryKey: qk.tickets });
    },
  });
  const move = useMutation({
    mutationFn: (v: { id: string; status: TicketStatus }) =>
      api.updateTicket(agent, v.id, v.status, ""),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.tickets }),
  });

  const byStatus = (st: TicketStatus) => tickets.filter((t) => t.status === st);
  const scrollTo = (st: string) =>
    boardRef.current
      ?.querySelector(`[data-col="${st}"]`)
      ?.scrollIntoView({ behavior: "smooth", block: "nearest", inline: "center" });

  return (
    <div className="mb-7">
      <SectionHeader
        className="mb-3"
        title={agent}
        meta={`${open} open`}
        action={
          <Button size="sm" variant="accent" onClick={() => setComposing((v) => !v)}>
            <Plus className="size-3" /> Ticket
          </Button>
        }
      />

      {composing && (
        <div className="mb-3 border border-dashed border-line2 p-3.5">
          <Input
            placeholder="Ticket title…"
            maxLength={200}
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="mb-2.5"
          />
          <Textarea
            placeholder={`Details for ${agent} (optional)…`}
            value={body}
            onChange={(e) => setBody(e.target.value)}
          />
          <div className="mt-2.5 flex items-stretch gap-2.5">
            <select
              aria-label="Lane"
              value={lane}
              onChange={(e) => setLane(e.target.value as TicketLane)}
              className="flex-none border border-line2 bg-bg px-3 py-2.5 font-mono text-[12px] uppercase tracking-wider text-ink focus:border-accent focus:outline-none"
            >
              <option value="">lane: default</option>
              <option value="hunt">hunt</option>
              <option value="improve">improve</option>
              <option value="ops">ops</option>
            </select>
            <Button
              variant="accent"
              className="flex-1"
              disabled={!title.trim() || create.isPending}
              onClick={() => create.mutate()}
            >
              File ticket
            </Button>
          </div>
          <ErrorText>{create.error instanceof Error ? create.error.message : null}</ErrorText>
        </div>
      )}

      {/* mobile column chips */}
      <div className="mb-3 flex gap-[7px] sm:hidden">
        {STATUSES.map((st) => (
          <button
            key={st}
            onClick={() => scrollTo(st)}
            className={cn(
              "flex flex-1 flex-col items-center gap-[3px] border border-line bg-surface px-1 py-[9px] font-mono text-[10px] font-bold uppercase tracking-wider text-mut",
              st === "open" && "border-accent/40",
              st === "review" && "border-review/40",
            )}
          >
            <span className="text-[15px] text-ink">{byStatus(st).length}</span>
            {st}
          </button>
        ))}
      </div>

      <div
        ref={boardRef}
        className="no-scrollbar -mx-4 flex snap-x snap-mandatory gap-2.5 overflow-x-auto px-4 pb-1 sm:mx-0 sm:grid sm:grid-cols-3 sm:overflow-visible sm:px-0"
      >
        {STATUSES.map((st) => {
          const all = byStatus(st);
          const shown = st === "closed" && all.length > 8 ? all.slice(0, 8) : all;
          const extra = all.length - shown.length;
          return (
            <div
              key={st}
              data-col={st}
              onDragOver={(e) => {
                e.preventDefault();
                setDropCol(st);
              }}
              onDragLeave={() => setDropCol((c) => (c === st ? null : c))}
              onDrop={(e) => {
                e.preventDefault();
                setDropCol(null);
                const [a, id] = e.dataTransfer.getData("text/plain").split("|");
                if (a !== agent) return;
                const t = tickets.find((x) => x.id === id);
                if (t && t.status !== st) move.mutate({ id, status: st });
              }}
              className={cn(
                "min-h-[120px] flex-[0_0_84%] snap-center border border-line bg-bg2 p-[9px] sm:flex-none",
                dropCol === st && "border-accent bg-surface",
              )}
            >
              <div className="mx-[3px] mb-2.5 flex justify-between font-mono text-[10px] font-bold uppercase tracking-widest text-mut">
                <span>{st}</span>
                <span className="text-ink">{all.length}</span>
              </div>
              {shown.length ? (
                shown.map((t) => (
                  <TicketCard
                    key={t.id}
                    t={t}
                    onOpen={() => navigate(`/tickets/${agent}/${encodeURIComponent(t.id)}`)}
                  />
                ))
              ) : (
                <div className="py-1 text-center font-mono text-[9.5px] uppercase tracking-wider text-dim">
                  empty
                </div>
              )}
              {extra > 0 && (
                <div className="py-1.5 text-center font-mono text-[9.5px] uppercase tracking-wider text-dim">
                  +{extra} older
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function TicketModal({ ticket }: { ticket: Ticket }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [note, setNote] = useState("");

  const update = useMutation({
    mutationFn: (status: string) => api.updateTicket(ticket.agent, ticket.id, status, note.trim()),
    onSuccess: () => {
      setNote("");
      qc.invalidateQueries({ queryKey: qk.tickets });
    },
  });

  const actions: [string, string][] =
    ticket.status === "open"
      ? [
          ["review", "Review"],
          ["closed", "Close"],
        ]
      : ticket.status === "review"
        ? [
            ["closed", "Close"],
            ["open", "Reopen"],
          ]
        : [["open", "Reopen"]];

  return (
    <Modal
      open
      onOpenChange={(o) => !o && navigate("/tickets")}
      title={
        <span className="inline-flex items-center gap-2.5">
          {ticket.title} {statusBadge(ticket.status)}
        </span>
      }
      subtitle={`${fmtRunId(ticket.id).label} · ${ticket.createdBy} · ${fmtAt(ticket.created)}`}
    >
      {ticket.body && <Markdown source={ticket.body} />}

      {!!ticket.notes?.length && (
        <div className="mt-2">
          {ticket.notes.map((n, i) => (
            <div key={i} className="my-2.5 border-l-2 border-line2 py-[3px] pl-3">
              <div className="font-mono text-[10px] font-bold uppercase tracking-wider text-mut">
                {n.by} · {fmtAt(n.at)}
              </div>
              <div className="mt-1 whitespace-pre-wrap break-words text-[13.5px]">{n.text}</div>
            </div>
          ))}
        </div>
      )}

      <div className="mt-4 flex flex-wrap gap-2 border-t border-line pt-3.5">
        <Textarea
          placeholder="Note (optional)…"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          className="mb-0.5 min-h-[52px] flex-[1_1_100%]"
        />
        {actions.map(([st, label]) => (
          <Button key={st} size="sm" disabled={update.isPending} onClick={() => update.mutate(st)}>
            {label}
          </Button>
        ))}
        <Button
          size="sm"
          disabled={update.isPending || !note.trim()}
          onClick={() => update.mutate("")}
        >
          Add note
        </Button>
      </div>
      <ErrorText>{update.error instanceof Error ? update.error.message : null}</ErrorText>
    </Modal>
  );
}

export function Tickets() {
  const { agent, id } = useParams();
  const { data, error } = useTickets();
  const groups = data ?? [];
  const selected =
    agent && id
      ? groups.find((g) => g.agent === agent)?.tickets?.find((t) => t.id === id)
      : undefined;

  return (
    <section>
      {groups.map((g) => (
        <AgentBoard key={g.agent} agent={g.agent} tickets={g.tickets ?? []} />
      ))}
      <ErrorText>{error instanceof Error ? error.message : null}</ErrorText>
      <div className="mt-2 max-w-[64ch] text-[11px] leading-relaxed text-dim">
        Open tickets ride each agent's nightly sweep. Agents move finished work to review — only you
        close.
      </div>
      {selected && <TicketModal ticket={selected} />}
    </section>
  );
}
