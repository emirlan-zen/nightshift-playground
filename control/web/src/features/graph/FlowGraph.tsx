import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import {
  Background,
  Controls,
  Handle,
  MiniMap,
  NodeToolbar,
  Panel,
  Position,
  ReactFlow,
  type Edge,
  type FinalConnectionState,
  type Node,
  type ReactFlowInstance,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Copy, GitBranch, Maximize2, Minimize2, Trash2 } from "lucide-react";
import type { FlowNodeDefinition } from "@/features/flows/api";
import type { GraphDraft, GraphIssue, GraphMode } from "./types";
import { NodeIcon } from "./nodeIcons";
import { ExecutorIcon } from "@/components/ExecutorIcon";
import { edgeTraveled } from "./model";

function Card({
  data,
}: {
  data: {
    id: string;
    label: string;
    icon?: string;
    state?: string;
    verdict?: string;
    sub?: string;
    // Executor identity (claude/codex) — implement-3's ExecutorIcon renders from this.
    executor?: string;
    // ADR-0023: emission budget (template views) / emitted-at-runtime (run views)
    // / member slot of a compared pair.
    emits?: { max: number; roles: string[]; fanIn: string };
    emitted: boolean;
    slot?: number;
    route: boolean;
    invalid: boolean;
    interactive: boolean;
    selected: boolean;
    onOpen?: () => void;
    // Edit mode: canvas-side actions on the selected node (toolbar next to it).
    onDuplicate?: () => void;
    onRoute?: () => void;
    onRemove?: () => void;
  };
}) {
  const toolbar = data.onDuplicate || data.onRoute || data.onRemove;
  return (
    <div
      className={`flow-graph-node ${data.state ?? ""} ${data.route ? "route" : ""} ${data.emitted ? "emitted" : ""} ${data.invalid ? "invalid" : ""} ${data.selected ? "selected" : ""}`}
      aria-invalid={data.invalid || undefined}
      data-testid={`graph-node-${data.id}`}
      // Run mode: nodes are tap targets that open the detail panel. The aria
      // label carries name + state only — live countdowns stay out of the
      // accessible name so screen readers don't re-announce every tick. The
      // explicit onClick/onKeyDown is load-bearing on touch: xyflow's own
      // onNodeClick can be swallowed by the pan gesture at phone widths, so the
      // node opens its detail here directly.
      {...(data.interactive
        ? {
            role: "button" as const,
            tabIndex: 0,
            "aria-pressed": data.selected,
            // Compared members share a name, so the slot is part of the
            // accessible name — otherwise two nodes announce identically.
            "aria-label": `${data.label}${data.slot ? ` slot ${data.slot}` : ""}, ${data.state ?? "pending"}. Open node details`,
            // stopPropagation so the ReactFlow onNodeClick below doesn't ALSO
            // fire — the selection is a toggle, and two calls per tap would
            // cancel out (open then immediately close).
            onClick: (e: MouseEvent) => {
              e.stopPropagation();
              data.onOpen?.();
            },
            onKeyDown: (e: KeyboardEvent) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                data.onOpen?.();
              }
            },
          }
        : {})}
    >
      {toolbar && (
        <NodeToolbar isVisible={data.selected} position={Position.Right} className="graph-toolbar">
          {data.onDuplicate && (
            <button
              aria-label={`Duplicate ${data.label} in parallel`}
              title="Duplicate in parallel (⌘D)"
              onClick={data.onDuplicate}
            >
              <Copy className="size-3.5" />
            </button>
          )}
          {data.onRoute && (
            <button
              aria-label={`Add exception route from ${data.label}`}
              title="On needs-work, append a route"
              onClick={data.onRoute}
            >
              <GitBranch className="size-3.5" />
            </button>
          )}
          {data.onRemove && (
            <button
              aria-label={`Remove ${data.label} from the canvas`}
              title="Remove (Del)"
              className="danger"
              onClick={data.onRemove}
            >
              <Trash2 className="size-3.5" />
            </button>
          )}
        </NodeToolbar>
      )}
      <Handle type="target" position={Position.Top} />
      <NodeIcon name={data.icon} className="size-6 text-accent" />
      <span>
        <b>
          {data.label}
          {/* Which member of a compared pair — the pair shares a name, so the
              slot is the only thing that tells them apart at a glance. */}
          {data.slot ? <em className="flow-graph-slot"> #{data.slot}</em> : null}
        </b>
        <small>
          {data.invalid ? "needs attention" : data.route ? "needs work" : (data.state ?? "node")}
        </small>
        {/* Verdict on its own line so it stays a distinct, glanceable signal. */}
        {data.verdict && <small className="flow-graph-verdict">{data.verdict}</small>}
        {/* Emission is a capability of the automation (how wide can this node go)
            and, on a run, the provenance of a node the run created itself. */}
        {data.emits && (
          <small className="flow-graph-emits">
            emits ≤{data.emits.max} → {data.emits.fanIn}
          </small>
        )}
        {data.emitted && <small className="flow-graph-emits">emitted</small>}
        {data.sub && <small aria-hidden="true">{data.sub}</small>}
      </span>
      {data.executor && <ExecutorIcon executor={data.executor} className="size-4 text-mut" />}
      <Handle type="source" position={Position.Bottom} />
    </div>
  );
}
const nodeTypes = { flow: Card };

const isTyping = (target: EventTarget | null) => {
  const el = target as HTMLElement | null;
  return Boolean(
    el &&
    (el.tagName === "INPUT" ||
      el.tagName === "TEXTAREA" ||
      el.tagName === "SELECT" ||
      el.isContentEditable),
  );
};

export function FlowGraph({
  draft,
  catalog,
  mode = "template-readonly",
  issues = [],
  selected = null,
  onDropRole,
  onSelect,
  onMoveNode,
  onRemoveNode,
  onDuplicateNode,
  onAddRouteFrom,
  onInsertRole,
  detail,
  className,
}: {
  draft: GraphDraft;
  catalog: FlowNodeDefinition[];
  mode?: GraphMode;
  issues?: GraphIssue[];
  selected?: string | null;
  onDropRole?: (role: string) => void;
  onSelect?: (id: string) => void;
  onMoveNode?: (id: string, stage: number, order: number) => void;
  /** Edit mode: remove a node from the canvas (toolbar + Delete key live in the editor). */
  onRemoveNode?: (id: string) => void;
  /** Edit mode: duplicate a node into its stage as a parallel member. */
  onDuplicateNode?: (id: string) => void;
  /** Edit mode: declare a needs-work exception route from this node's role. */
  onAddRouteFrom?: (id: string) => void;
  /** Edit mode: drag from a node's handle onto the canvas → pick a role to insert as a stage. */
  onInsertRole?: (nodeId: string, role: string, side: "after" | "before") => void;
  /** Rendered as an overlay panel while expanded, so node details stay reachable in fullscreen. */
  detail?: ReactNode;
  /** Extra classes on the canvas wrapper (e.g. "compact" for in-form previews). */
  className?: string;
}) {
  const wrapperRef = useRef<HTMLDivElement>(null);
  const instanceRef = useRef<ReactFlowInstance | null>(null);
  // Fullscreen is a CSS overlay, not the Fullscreen API — iPhones don't support
  // the API for non-video elements, and the app is phone-first.
  const [expanded, setExpanded] = useState(false);
  const [picker, setPicker] = useState<{
    x: number;
    y: number;
    nodeId: string;
    side: "after" | "before";
  } | null>(null);
  const [pickerQuery, setPickerQuery] = useState("");

  useEffect(() => {
    // Re-fit after the wrapper jumps between inline and fullscreen bounds.
    const raf = requestAnimationFrame(() => instanceRef.current?.fitView({ padding: 0.16 }));
    if (!expanded) return () => cancelAnimationFrame(raf);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      cancelAnimationFrame(raf);
      document.body.style.overflow = prev;
    };
  }, [expanded]);

  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (isTyping(e.target)) return;
      if (e.key === "Escape") {
        if (picker) setPicker(null);
        else if (expanded) setExpanded(false);
        return;
      }
      if ((e.key === "f" || e.key === "F") && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        setExpanded((x) => !x);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [expanded, picker]);

  const { nodes, edges } = useMemo(() => {
    const happy = draft.nodes.filter((n) => n.lane === "happy");
    const invalidNode = (n: (typeof draft.nodes)[number]) =>
      issues.some(
        (issue) =>
          issue.role === n.role ||
          issue.stage === n.stage ||
          (issue.edge !== undefined && n.lane === "route" && n.id.startsWith(`r:${issue.edge}:`)),
      );
    const stateOf = (id: string) => draft.nodes.find((n) => n.id === id)?.state;
    const ns: Node[] = draft.nodes.map((n) => {
      const def = catalog.find((d) => d.id === n.role);
      return {
        id: n.id,
        type: "flow",
        position: { x: n.lane === "route" ? 520 + n.order * 190 : n.order * 200, y: n.stage * 130 },
        data: {
          id: n.id,
          label: n.label ?? def?.name ?? n.role,
          // Profile waves aren't catalog nodes (no def); fall back to the role
          // name so medic/scout/review/plan hit the glyph map instead of wrench.
          icon: def?.icon ?? n.role,
          state: n.state,
          verdict: n.verdict,
          // Template views surface each node's runtime on the card — per-node
          // executor/effort/minutes are the levers, so they stay glanceable.
          // Template cards show the runtime that would actually be used: a member
          // pin (ADR-0023) wins over the definition's default.
          sub:
            n.sub ??
            (mode !== "run" && def
              ? `${n.effort ?? def.effort} · ${n.minutes ?? def.minutes}m`
              : undefined),
          executor: n.executor ?? (mode !== "run" ? def?.executor : undefined),
          emits: n.emits,
          emitted: Boolean(n.emittedBy),
          slot: n.slot,
          route: n.lane === "route",
          invalid: invalidNode(n),
          // Read-only previews with a selection handler (the create form) get
          // the same direct tap target as run mode — xyflow's onNodeClick can
          // be swallowed by the pan gesture at phone widths.
          interactive: mode === "run" || (mode === "template-readonly" && !!onSelect),
          selected: n.id === selected,
          onOpen:
            mode === "run" || (mode === "template-readonly" && onSelect)
              ? () => onSelect?.(n.id)
              : undefined,
          onDuplicate:
            mode === "edit" && onDuplicateNode && n.lane === "happy"
              ? () => onDuplicateNode(n.id)
              : undefined,
          onRoute:
            mode === "edit" && onAddRouteFrom && n.lane === "happy"
              ? () => onAddRouteFrom(n.id)
              : undefined,
          onRemove: mode === "edit" && onRemoveNode ? () => onRemoveNode(n.id) : undefined,
        },
        draggable: mode === "edit",
      };
    });
    const es: Edge[] = [];
    const stages = [...new Set(happy.map((n) => n.stage))].sort((a, b) => a - b);
    stages.slice(1).forEach((stage, i) =>
      happy
        .filter((n) => n.stage === stages[i])
        .forEach((a) =>
          happy
            .filter((n) => n.stage === stage)
            .forEach((b) =>
              es.push({
                id: `${a.id}-${b.id}`,
                source: a.id,
                target: b.id,
                className: issues.some((issue) => issue.stage === stage)
                  ? "invalid"
                  : mode === "run" && edgeTraveled(a.state)
                    ? "traveled"
                    : "",
              }),
            ),
        ),
    );
    draft.routes.forEach((r, ri) => {
      const sources = happy.filter((n) => n.role === r.node),
        route = draft.nodes.filter((n) => n.lane === "route" && n.id.startsWith(`r:${ri}:`)),
        bad = issues.some((issue) => issue.edge === ri);
      sources.forEach(
        (s) =>
          route[0] &&
          es.push({
            id: `nw-${s.id}-${ri}`,
            source: s.id,
            target: route[0].id,
            label: "needs work",
            className: `needs-work${bad ? " invalid" : ""}${mode === "run" && edgeTraveled(stateOf(s.id)) && edgeTraveled(route[0].state) ? " traveled" : ""}`,
          }),
      );
      route.slice(1).forEach((n, i) =>
        es.push({
          id: `nw-${ri}-${i}`,
          source: route[i].id,
          target: n.id,
          className: `needs-work${bad ? " invalid" : ""}`,
        }),
      );
    });
    return { nodes: ns, edges: es };
  }, [
    draft,
    catalog,
    mode,
    issues,
    selected,
    onSelect,
    onDuplicateNode,
    onAddRouteFrom,
    onRemoveNode,
  ]);

  const canConnect = mode === "edit" && !!onInsertRole;
  const onConnectEnd = (
    event: globalThis.MouseEvent | globalThis.TouchEvent,
    state: FinalConnectionState,
  ) => {
    if (!canConnect || !state.fromNode || !wrapperRef.current) return;
    const rect = wrapperRef.current.getBoundingClientRect();
    const point = "changedTouches" in event ? event.changedTouches[0] : event;
    if (!point) return;
    setPickerQuery("");
    setPicker({
      x: Math.min(Math.max(point.clientX - rect.left, 8), rect.width - 248),
      y: Math.min(Math.max(point.clientY - rect.top, 8), rect.height - 200),
      nodeId: state.fromNode.id,
      side: state.fromHandle?.type === "target" ? "before" : "after",
    });
  };
  const pickerMatches = picker
    ? catalog.filter(
        (n) =>
          !pickerQuery ||
          n.name.toLowerCase().includes(pickerQuery.toLowerCase()) ||
          n.id.includes(pickerQuery.toLowerCase()),
      )
    : [];

  return (
    <div
      ref={wrapperRef}
      className={`flow-graph${className ? ` ${className}` : ""}${expanded ? " fullscreen" : ""}`}
      data-testid="graph-canvas"
      onDragOver={(e) => {
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
      }}
      onDrop={(e) => {
        e.preventDefault();
        const role = e.dataTransfer.getData("application/nightshift-node");
        if (role) onDropRole?.(role);
      }}
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onInit={(instance) => {
          instanceRef.current = instance;
        }}
        fitView
        fitViewOptions={{ padding: 0.16 }}
        minZoom={0.3}
        maxZoom={1.6}
        nodesConnectable={canConnect}
        // Happy-path edges are derived from stage adjacency and routes are data
        // in the automation — a raw handle-to-handle connection has no meaning,
        // so every drag ends "invalid" and lands in the insert picker instead.
        isValidConnection={() => false}
        onConnectEnd={onConnectEnd}
        // The editor owns Delete/arrows semantically (stages + order, not x/y),
        // so xyflow's built-in keyboard movement and deletion stay off.
        deleteKeyCode={null}
        disableKeyboardA11y
        onNodeClick={(_, n) => onSelect?.(n.id)}
        onNodeDragStop={(_, node) =>
          onMoveNode?.(
            node.id,
            Math.max(0, Math.round(node.position.y / 130)),
            Math.max(0, Math.round(node.position.x / 200)),
          )
        }
        panOnDrag
      >
        <Background />
        <Controls showInteractive={false} />
        {expanded && <MiniMap pannable zoomable />}
        <Panel position="top-right">
          <button
            className="graph-fab"
            data-testid="graph-expand"
            aria-label={expanded ? "Exit fullscreen canvas" : "Expand canvas to fullscreen"}
            aria-pressed={expanded}
            title={expanded ? "Exit fullscreen (Esc)" : "Fullscreen (F)"}
            onClick={() => setExpanded((x) => !x)}
          >
            {expanded ? <Minimize2 className="size-4" /> : <Maximize2 className="size-4" />}
          </button>
        </Panel>
        {expanded && issues.length > 0 && (
          <Panel position="top-center" className="graph-issues" data-testid="graph-issues-overlay">
            {issues[0].message}
            {issues.length > 1 ? ` · +${issues.length - 1} more` : ""}
          </Panel>
        )}
        {expanded && mode === "edit" && onDropRole && (
          <Panel position="bottom-center" className="graph-palette-wrap">
            <p className="graph-hints">
              drag handle → insert · del remove · ⌘z undo · ⌘d parallel · f fullscreen
            </p>
            <div className="graph-palette" aria-label="Node library">
              {catalog.map((node) => (
                <button
                  key={node.id}
                  draggable
                  data-testid={`canvas-palette-${node.id}`}
                  onDragStart={(e) => {
                    e.dataTransfer.setData("application/nightshift-node", node.id);
                    e.dataTransfer.effectAllowed = "copy";
                  }}
                  onClick={() => onDropRole(node.id)}
                >
                  <NodeIcon name={node.icon} className="size-4 text-accent" />
                  {node.name}
                </button>
              ))}
            </div>
          </Panel>
        )}
        {expanded && detail && (
          <Panel position="top-left" className="graph-detail">
            {detail}
          </Panel>
        )}
      </ReactFlow>
      {picker && (
        <div
          className="graph-picker"
          style={{ left: picker.x, top: picker.y }}
          data-testid="graph-node-picker"
        >
          <input
            autoFocus
            value={pickerQuery}
            placeholder={picker.side === "before" ? "Insert stage before…" : "Insert stage after…"}
            aria-label="Search nodes to insert"
            onChange={(e) => setPickerQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setPicker(null);
              if (e.key === "Enter" && pickerMatches[0]) {
                onInsertRole?.(picker.nodeId, pickerMatches[0].id, picker.side);
                setPicker(null);
              }
            }}
          />
          <div className="graph-picker-list">
            {pickerMatches.map((node) => (
              <button
                key={node.id}
                data-testid={`picker-node-${node.id}`}
                onClick={() => {
                  onInsertRole?.(picker.nodeId, node.id, picker.side);
                  setPicker(null);
                }}
              >
                <NodeIcon name={node.icon} className="size-4 text-accent" />
                <span>
                  {node.name}
                  <small>
                    {node.effort} · {node.minutes}m
                  </small>
                </span>
              </button>
            ))}
            {!pickerMatches.length && <p>No nodes match.</p>}
          </div>
        </div>
      )}
    </div>
  );
}
