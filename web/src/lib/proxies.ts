import type { CredentialView, ProxiesResponse, ProxyCheckResult, ProxyCheckTarget, ProxySaveRequest, ProxyView } from "../api/types";
import type { Params } from "../i18n";
import { isDirect } from "./credentials";

/** Check result keys: "id:<proxy id>" for list proxies, plus the two built-in rows. */
export const INHERIT_KEY = "inherit";
export const DIRECT_KEY = "direct";

export function proxyCheckKey(id: string): string {
  return "id:" + id;
}

/** The check request body for a result key. */
export function checkTarget(key: string): ProxyCheckTarget {
  if (key.startsWith("id:")) return { id: key.slice(3) };
  return { target: key === DIRECT_KEY ? "direct" : "inherit" };
}

/**
 * An egress check shown in the list. The page keeps checks in flight and
 * failed requests itself; finished checks are saved by the plugin and come
 * back with the list (see savedCheck). url is the address the check ran
 * against, so a result is dropped once the proxy's address changes.
 */
export interface ProxyCheckEntry {
  state: "loading" | "ok" | "error";
  url: string;
  result?: ProxyCheckResult;
  error?: string;
}

export type ProxyCheckMap = Record<string, ProxyCheckEntry | undefined>;

/** The address a result key currently points at. */
export function checkUrl(key: string, proxies: ProxyView[], globalProxy: string | undefined): string | null {
  if (key === DIRECT_KEY) return "";
  if (key === INHERIT_KEY) return globalProxy || "";
  const proxy = proxies.find((p) => proxyCheckKey(p.id) === key);
  return proxy ? proxy.url : null;
}

/** Drops results of deleted proxies and of rows whose address changed. */
export function pruneChecks(checks: ProxyCheckMap, proxies: ProxyView[], globalProxy: string | undefined): ProxyCheckMap {
  const out: ProxyCheckMap = {};
  for (const [key, entry] of Object.entries(checks)) {
    if (!entry) continue;
    const url = checkUrl(key, proxies, globalProxy);
    if (url !== null && url === entry.url) out[key] = entry;
  }
  return out;
}

/** How many usable keys sit on each egress choice. */
export interface ProxyUsage {
  inherit: number;
  direct: number;
  /** Keys with an address that matches no list proxy. */
  custom: number;
  byId: Map<string, number>;
}

export function proxyUsage(creds: CredentialView[]): ProxyUsage {
  const usage: ProxyUsage = { inherit: 0, direct: 0, custom: 0, byId: new Map() };
  for (const cred of creds) {
    if (cred.read_error) continue;
    const proxy = String(cred.proxy_url || "").trim();
    if (cred.proxy_id) usage.byId.set(cred.proxy_id, (usage.byId.get(cred.proxy_id) || 0) + 1);
    else if (!proxy) usage.inherit++;
    else if (isDirect(proxy)) usage.direct++;
    else usage.custom++;
  }
  return usage;
}

/** Keys whose proxy address is not in the list yet (the import button's work). */
export function unlistedProxyKeys(creds: CredentialView[]): CredentialView[] {
  return creds.filter((c) => !c.read_error && !c.proxy_id && !!String(c.proxy_url || "").trim() && !isDirect(c.proxy_url));
}

/** The last check the plugin saved for a row. */
export function savedCheck(key: string, data: ProxiesResponse | null): ProxyCheckResult | undefined {
  if (!data) return undefined;
  if (key === DIRECT_KEY || key === INHERIT_KEY) return data.builtin_checks ? data.builtin_checks[key] : undefined;
  const proxy = data.proxies.find((p) => proxyCheckKey(p.id) === key);
  return proxy ? proxy.last_check : undefined;
}

/** What a row shows: a check in flight or a failed request first, else the saved result. */
export function shownCheck(key: string, local: ProxyCheckMap, data: ProxiesResponse | null): ProxyCheckEntry | undefined {
  const entry = local[key];
  if (entry) return entry;
  const result = savedCheck(key, data);
  if (!result) return undefined;
  return { state: result.ok ? "ok" : "error", url: "", result, error: result.error || "" };
}

/** The list with a finished check stored on its row, as the plugin saved it. */
export function withSavedCheck(data: ProxiesResponse, key: string, result: ProxyCheckResult): ProxiesResponse {
  if (key === DIRECT_KEY || key === INHERIT_KEY) {
    return { ...data, builtin_checks: { ...(data.builtin_checks || {}), [key]: result } };
  }
  return { ...data, proxies: data.proxies.map((p) => (proxyCheckKey(p.id) === key ? { ...p, last_check: result } : p)) };
}

const regionNames = new Map<string, Intl.DisplayNames | null>();

/** The country's name in the page language ("香港", "Japan"); the code itself when unknown. */
export function countryName(code: string | undefined, locale: string): string {
  const raw = String(code || "").trim();
  if (!/^[A-Za-z]{2}$/.test(raw)) return raw;
  const upper = raw.toUpperCase();
  if (!regionNames.has(locale)) {
    let names: Intl.DisplayNames | null = null;
    try {
      names = new Intl.DisplayNames([locale], { type: "region", style: "short" });
    } catch {
      names = null;
    }
    regionNames.set(locale, names);
  }
  const names = regionNames.get(locale);
  try {
    return (names && names.of(upper)) || upper;
  } catch {
    return upper;
  }
}

/** Country · city (or region) of a successful check. */
export function egressPlace(result: Pick<ProxyCheckResult, "country" | "region" | "city"> | undefined, locale: string): string {
  if (!result) return "";
  const place = result.city || result.region || "";
  return [countryName(result.country, locale), place].map((v) => String(v || "").trim()).filter(Boolean).join(" · ");
}

/** A check time: the time of day when it is today, else month, day and time. */
export function checkedAtText(iso: string, locale: string, now: Date = new Date()): string {
  const at = new Date(iso);
  if (isNaN(at.valueOf())) return iso;
  if (at.toDateString() === now.toDateString()) {
    return at.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
  }
  const sameYear = at.getFullYear() === now.getFullYear();
  return at.toLocaleString(locale, {
    year: sameYear ? undefined : "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false,
  });
}

/** The plugin serves Twemoji flags by country code. */
export const FLAG_URL = "/v0/resource/plugins/wokey/flag.svg";

/** The flag image for a two-letter country code, or "" when there is none. */
export function flagUrl(code: string | undefined): string {
  const raw = String(code || "").trim();
  return /^[A-Za-z]{2}$/.test(raw) ? FLAG_URL + "?cc=" + raw.toLowerCase() : "";
}

/** The egress a check went through, as the message key and params to show. */
export function egressName(egress: string | undefined): { key: string; params: Params | null } | null {
  if (!egress) return null;
  if (egress === "direct") return { key: "proxy.direct", params: null };
  if (egress === "env") return { key: "logs.egress.env", params: null };
  return { key: "proxies.via", params: { egress } };
}

/** Runs fn over items, at most limit at a time, and resolves when all are done. */
export async function runLimited<T>(items: readonly T[], limit: number, fn: (item: T) => Promise<unknown>): Promise<void> {
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const item = items[next++];
      try {
        await fn(item);
      } catch {
        /* each item reports its own failure */
      }
    }
  };
  await Promise.all(Array.from({ length: Math.max(1, Math.min(limit, items.length)) }, worker));
}

/** The add/edit proxy dialog. */
export interface ProxyDialogState {
  seq: number;
  mode: "add" | "edit";
  id: string;
  title: string;
  name: string;
  /** Typed address; when editing, empty keeps the saved one. */
  url: string;
  origName: string;
  /** The saved address with its password redacted, shown as the placeholder. */
  origUrl: string;
  usedBy: number;
  error: { key: string; params: Params | null } | null;
}

export function newProxyDialog(seq: number, proxy: ProxyView | null, usedBy = 0): ProxyDialogState {
  return {
    seq,
    mode: proxy ? "edit" : "add",
    id: proxy ? proxy.id : "",
    title: proxy ? proxy.name || proxy.label : "",
    name: proxy ? proxy.name : "",
    url: "",
    origName: proxy ? proxy.name : "",
    origUrl: proxy ? proxy.url : "",
    usedBy: proxy ? usedBy : 0,
    error: null,
  };
}

export type ProxyBodyResult = { body: ProxySaveRequest; changed: boolean; urlChanged: boolean } | { error: string };

/** The request body: when editing, only what changed (an empty address keeps the saved one). */
export function buildProxyBody(d: ProxyDialogState): ProxyBodyResult {
  const name = d.name.trim();
  const url = d.url.trim();
  if (d.mode === "add") {
    if (!url) return { error: "proxies.dialog.needUrl" };
    return { body: { name, url }, changed: true, urlChanged: true };
  }
  const body: ProxySaveRequest = { id: d.id };
  if (name !== d.origName) body.name = name;
  if (url) body.url = url;
  return { body, changed: body.name !== undefined || body.url !== undefined, urlChanged: body.url !== undefined };
}
