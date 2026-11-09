import type { CredentialView, LogEntry } from "../api/types";
import { fileStem } from "./format";

export const LOG_SIZE_KEY = "cliproxy-wokey-log-size";
export const LOG_SIZES = [20, 50, 100, 200];
export const DEFAULT_LOG_SIZE = 50;
export const LOG_RANGE_KEY = "cliproxy-wokey-log-range";
export const LOG_RANGES = ["all", "today", "24h", "7d", "30d", "custom"] as const;
export type LogRange = (typeof LOG_RANGES)[number];

const LOCAL_MINUTE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/;
const DAY_MS = 86400000;

export function isLogRange(value: unknown): value is LogRange {
  return (LOG_RANGES as readonly unknown[]).includes(value);
}

/** A saved page size, or the default when it is not one of LOG_SIZES. */
export function parseLogSize(saved: string | null): number {
  const n = Number(saved);
  return LOG_SIZES.includes(n) ? n : DEFAULT_LOG_SIZE;
}

/** Formats a date for a datetime-local input (local time, to the minute). */
export function localMinute(date: Date): string {
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
}

/** The start of the next minute after now. */
export function nextMinute(now: number): Date {
  return new Date(Math.floor(now / 60000) * 60000 + 60000);
}

/** Checks a pair of datetime-local values; "" when they form a valid range. */
export function customRangeError(start: string, end: string): string {
  if (!LOCAL_MINUTE.test(start) || !LOCAL_MINUTE.test(end)) return "logs.range.incomplete";
  const from = new Date(start).getTime();
  const to = new Date(end).getTime();
  if (!Number.isFinite(from) || !Number.isFinite(to)) return "logs.range.incomplete";
  return from < to ? "" : "logs.range.order";
}

export interface RangeSelection {
  range: LogRange;
  /** Applied custom bounds (datetime-local values). */
  start: string;
  end: string;
}

/**
 * Turns the selected range into from/to query parameters. Relative ranges
 * are computed against now; null means the custom range is not usable.
 */
export function logRangeParams(sel: RangeSelection, now: number): Record<string, string> | null {
  switch (sel.range) {
    case "custom":
      if (customRangeError(sel.start, sel.end)) return null;
      return { from: new Date(sel.start).toISOString(), to: new Date(sel.end).toISOString() };
    case "today": {
      const start = new Date(now);
      start.setHours(0, 0, 0, 0);
      return { from: start.toISOString() };
    }
    case "24h":
    case "7d":
    case "30d": {
      const days = { "24h": 1, "7d": 7, "30d": 30 }[sel.range];
      return { from: new Date(nextMinute(now).getTime() - days * DAY_MS).toISOString() };
    }
    default:
      return {};
  }
}

/**
 * The range to start with: the saved one when valid. The custom bounds
 * default to the last 24 hours and take the saved ones only when they form a
 * valid range.
 */
export function restoreLogRange(savedJSON: string | null, now: number): RangeSelection {
  const end = nextMinute(now);
  const sel: RangeSelection = { range: "all", start: localMinute(new Date(end.getTime() - DAY_MS)), end: localMinute(end) };
  try {
    const saved = JSON.parse(savedJSON || "null") as { range?: unknown; start?: unknown; end?: unknown } | null;
    if (saved && isLogRange(saved.range)) {
      sel.range = saved.range;
      if (!customRangeError(String(saved.start || ""), String(saved.end || ""))) {
        sel.start = saved.start as string;
        sel.end = saved.end as string;
      }
    }
  } catch {
    /* ignore */
  }
  return sel;
}

export type LatTone = "" | "good" | "warn" | "bad";

/** Colours a timing the way ClinePassBridge does. */
export function latTone(ms: number | null | undefined, kind: "ttft" | "total", outTokens: number): LatTone {
  if (!(typeof ms === "number" && ms > 0)) return "";
  const sec = ms / 1000;
  if (kind === "ttft") return sec < 5 ? "good" : sec < 10 ? "warn" : "bad";
  if (outTokens >= 100) {
    const rate = outTokens / sec;
    return rate >= 30 ? "good" : rate >= 15 ? "warn" : "bad";
  }
  return sec < 10 ? "good" : sec < 30 ? "warn" : "bad";
}

export interface PagerView {
  pages: number;
  /** Zero-based page of the offset. */
  current: number;
}

export function pagerView(total: number, offset: number, size: number): PagerView {
  const pages = Math.max(1, Math.ceil(total / size));
  return { pages, current: Math.min(pages - 1, Math.floor(offset / size)) };
}

/** Page buttons to show: the first, the last and two on each side of the current page. */
export function pagerNumbers(current: number, pages: number): number[] {
  const set = new Set([0, pages - 1]);
  for (let n = current - 2; n <= current + 2; n++) if (n >= 0 && n < pages) set.add(n);
  return [...set].sort((a, b) => a - b);
}

/** The offset that keeps the first entry of the page in view after a size change. */
export function offsetForSize(offset: number, size: number): number {
  return Math.floor(offset / size) * size;
}

/** The offset of the last page, for a page that went past the end. */
export function lastPageOffset(total: number, size: number): number {
  return Math.floor((total - 1) / size) * size;
}

/**
 * The 1-based page a jump box asks for, clamped to the pages; null when the
 * box is empty or not a number.
 */
export function jumpTarget(value: string, pages: number): number | null {
  const n = Math.round(Number(value));
  if (!Number.isFinite(n) || value === "") return null;
  return Math.min(pages, Math.max(1, n));
}

/** CPA's request ID in full; "—" for entries without one. */
export function logRequestID(e: Pick<LogEntry, "request_id">): string {
  return String(e.request_id || "").trim() || "—";
}

/**
 * The current credential behind an entry, found by its auth file; entries
 * of deleted credentials have none.
 */
export function logCred(e: Pick<LogEntry, "auth_id">, creds: CredentialView[]): CredentialView | null {
  return e.auth_id ? creds.find((c) => c.id === e.auth_id || c.name === e.auth_id) || null : null;
}

/** The auth index stored with the entry (0.1.9+), else the current credential's. */
export function logAuthIndex(e: Pick<LogEntry, "auth_id" | "auth_index">, creds: CredentialView[]): string {
  if (e.auth_index) return e.auth_index;
  const cred = logCred(e, creds);
  return (cred && cred.auth_index) || "—";
}

/** Whether a stored credential value is a masked key ("sk-wk-…aaaa", "sk****"). */
export function isMaskedKey(value: string | undefined): boolean {
  return /…|\*{4}/.test(value || "");
}

/**
 * The masked key of an entry: the one stored with it, else the current
 * credential's. Builds up to 0.1.8 stored the credential's label (or, in
 * early 0.1.7, the auth file name) when it had one, so only a masked value
 * counts.
 */
export function logKey(e: Pick<LogEntry, "auth_id" | "credential">, creds: CredentialView[]): string {
  if (isMaskedKey(e.credential)) return e.credential || "";
  const cred = logCred(e, creds);
  return (cred && cred.key_masked) || "";
}

/**
 * The label of the credential behind an entry, when it says more than the
 * file name. Deleted credentials fall back to the label that builds up to
 * 0.1.8 stored.
 */
export function logLabel(e: Pick<LogEntry, "auth_id" | "credential">, creds: CredentialView[]): string {
  const cred = logCred(e, creds);
  const stored = e.credential && e.credential !== e.auth_id && !isMaskedKey(e.credential) ? e.credential : "";
  const label = (cred ? cred.label || "" : stored).trim();
  return label && label !== fileStem(e.auth_id) && label !== fileStem(cred && cred.name) ? label : "";
}

/** Names the key behind an entry by its auth file, without the extension. */
export function logCredential(e: Pick<LogEntry, "auth_id" | "credential">, creds: CredentialView[]): string {
  return fileStem(e.auth_id) || logKey(e, creds) || "—";
}

/** Cache hit share of one entry (0..1); null without input tokens. */
export function cacheRate(e: Pick<LogEntry, "input_tokens" | "cached_tokens">): number | null {
  return e.input_tokens > 0 ? e.cached_tokens / e.input_tokens : null;
}

/** Whether a log query narrows the full log (any range, status or search). */
export function isFiltered(range: LogRange, status: string, search: string): boolean {
  return range !== "all" || status !== "all" || search.trim() !== "";
}

const FINISH_GROUPS: Record<string, string> = {
  stop: "stop",
  end_turn: "stop",
  stop_sequence: "stop",
  completed: "stop",
  length: "length",
  max_tokens: "length",
  max_output_tokens: "length",
  model_context_window_exceeded: "length",
  tool_use: "tool",
  tool_calls: "tool",
  function_call: "tool",
  content_filter: "filter",
  refusal: "filter",
  failed: "failed",
  cancelled: "cancelled",
};

/**
 * The i18n key that explains a finish reason, "" for reasons it does not
 * know (they are shown as they are).
 */
export function finishKey(reason: string | undefined): string {
  const group = FINISH_GROUPS[String(reason || "").trim().toLowerCase()];
  return group ? "logs.finish." + group : "";
}

/** When the request finished: its start plus the duration; null without a valid start. */
export function logEnd(e: Pick<LogEntry, "time" | "duration_ms">): Date | null {
  const start = new Date(e.time).getTime();
  return Number.isFinite(start) ? new Date(start + Math.max(0, e.duration_ms || 0)) : null;
}

/** Whether the response said nothing about usage, so the zero token counts mean unknown. */
export function usageMissing(e: Pick<LogEntry, "usage_reported">): boolean {
  return e.usage_reported === false;
}

/** How many credentials CPA tried for the request; 1 for entries without a request ID. */
export function attemptCount(e: Pick<LogEntry, "attempts" | "request_id">): number {
  return e.request_id && (e.attempts || 0) > 1 ? (e.attempts as number) : 1;
}
