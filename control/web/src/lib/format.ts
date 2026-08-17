import { marked } from "marked";
import DOMPurify from "dompurify";

const p = (n: number) => String(n).padStart(2, "0");

/** "dd.mm HH:MM" in the viewer's local (operator = Bishkek) time. */
export function fmtAt(iso: string | number): string {
  const d = new Date(iso);
  return `${p(d.getDate())}.${p(d.getMonth() + 1)} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** remaining time until a unix-seconds deadline, e.g. "23h 45m". */
export function fmtLeft(untilSec: number): string {
  const s = Math.max(0, untilSec - Date.now() / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return (h ? `${h}h ` : "") + `${m}m`;
}

/** datetime-local value for the next 02:00 (default night-run slot). */
export function nextNight(): string {
  const d = new Date();
  d.setHours(2, 0, 0, 0);
  if (d <= new Date()) d.setDate(d.getDate() + 1);
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

const MONTHS = "Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec".split(" ");

/**
 * Humanize a run / report / ticket id. The id stays the stable on-box key
 * (filename, sidecar, obs correlation) — this only makes it readable in the UI.
 * "20260705-0500-sweep-ef56" -> { label: "Sweep · Jul 5, 05:00", hash: "ef56" }.
 * Non-matching ids fall through unchanged so nothing ever renders blank.
 */
export function fmtRunId(id: string): { label: string; hash: string; raw: string } {
  const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})-([a-z0-9]+)-([0-9a-f]+)$/i.exec(id);
  if (!m) return { label: id, hash: "", raw: id };
  const [, , mo, d, hh, mm, kind, hash] = m;
  const month = MONTHS[Number(mo) - 1] ?? mo;
  const k = kind === "tkt" ? "ticket" : kind;
  const label = `${k[0].toUpperCase()}${k.slice(1)} · ${month} ${Number(d)}, ${hh}:${mm}`;
  return { label, hash, raw: id };
}

/**
 * Which "night" a timestamp belongs to, evening-anchored: a run at 00:45 or a
 * report written at 08:03 belongs to the PREVIOUS evening's night. Shift back
 * 12h so noon is the boundary, then take the local (Bishkek) date. Key sorts
 * lexically, newest last. Pass unix-seconds fields as ms (mtime * 1000).
 */
export function nightKey(iso: string | number): string {
  const d = new Date(iso);
  d.setHours(d.getHours() - 12);
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** "2026-07-05" -> "Night of Jul 5". Falls through unchanged if unparseable. */
export function nightLabel(key: string): string {
  const [y, m, day] = key.split("-").map(Number);
  if (!y || !m || !day) return key;
  return `Night of ${MONTHS[m - 1] ?? m} ${day}`;
}

/** compact token count: 1_608_819_803 -> "1.61B", 484_559 -> "485K". */
export function fmtTokens(n: number): string {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

/** thousands-separated integer. */
export function fmtInt(n: number): string {
  return n.toLocaleString("en-US");
}

/** compact API-equivalent USD: 1834.2 -> "$1,834", 12.4 -> "$12.40", 0 -> "$0". */
export function fmtUSD(n: number): string {
  if (n >= 100) return "$" + Math.round(n).toLocaleString("en-US");
  if (n >= 1) return "$" + n.toFixed(2);
  return n > 0 ? "$" + n.toFixed(2) : "$0";
}

/** bytes -> binary units: 71 * 2**30 -> "71.0 GB", 512 * 2**20 -> "512 MB". */
export function fmtBytes(n: number): string {
  if (n >= 2 ** 40) return (n / 2 ** 40).toFixed(1) + " TB";
  if (n >= 2 ** 30) return (n / 2 ** 30).toFixed(1) + " GB";
  if (n >= 2 ** 20) return Math.round(n / 2 ** 20) + " MB";
  if (n >= 2 ** 10) return Math.round(n / 2 ** 10) + " KB";
  return n + " B";
}

/** seconds -> coarse uptime: 540072 -> "6d 6h", 4200 -> "1h 10m", 40 -> "40s". */
export function fmtUptime(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m`;
  return `${Math.floor(sec)}s`;
}

const MODELS = ["opus", "sonnet", "haiku", "fable"];
export function shortModel(m?: string): string {
  if (!m) return "";
  const low = m.toLowerCase();
  for (const k of MODELS) if (low.includes(k)) return k;
  return low;
}

marked.setOptions({ gfm: true, breaks: false });

/** render trusted (agent-authored, behind CF Access) markdown to sanitized HTML. */
export function renderMarkdown(src: string): string {
  const raw = marked.parse(src, { async: false }) as string;
  return DOMPurify.sanitize(raw, { ADD_ATTR: ["target", "rel"] });
}
