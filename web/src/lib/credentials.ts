import type { CredentialView, IssuerView, KeyUsageView, ModelView, PluginConfigView, QuotaView, StatusView } from "../api/types";
import { fileStem } from "./format";

export interface QuotaEntry {
  state: "loading" | "ok" | "error";
  data?: QuotaView;
  error?: string;
}

export type QuotaMap = Record<string, QuotaEntry | undefined>;

export function displayName(cred: Pick<CredentialView, "label" | "name" | "key_masked">): string {
  return cred.label || fileStem(cred.name) || cred.key_masked;
}

export function isDirect(proxy: string | undefined | null): boolean {
  const value = String(proxy || "").trim().toLowerCase();
  return value === "direct" || value === "none";
}

/** The credentials of a status that listed fine. */
export function credentialsOf(status: StatusView | null): CredentialView[] {
  return status && !status.list_error && Array.isArray(status.credentials) ? status.credentials : [];
}

/** Every credential of a status, even when listing reported an error. */
export function allCredentials(status: StatusView | null): CredentialView[] {
  return status && Array.isArray(status.credentials) ? status.credentials : [];
}

/** The plugin's default model prefix; "wokey" before the status arrived. */
export function defaultPrefix(status: StatusView | null): string {
  return status && status.config ? status.config.default_prefix || "" : "wokey";
}

export type BadgeClass = "danger" | "" | "warn" | "ok";

/** Badge class and message key of a credential's state. */
export function credState(cred: CredentialView): [BadgeClass, string] {
  if (cred.read_error) return ["danger", "state.readError"];
  if (cred.disabled) return ["", "state.disabled"];
  if (cred.unavailable) return ["warn", "state.unavailable"];
  return ["ok", "state.enabled"];
}

/** filter is already trimmed and lower-cased. */
export function credMatches(cred: CredentialView, filter: string): boolean {
  if (!filter) return true;
  return [cred.label, cred.key_masked, cred.name, cred.effective_prefix, cred.note]
    .some((value) => String(value || "").toLowerCase().includes(filter));
}

export type HealthState = "unknown" | "ok" | "warn" | "error";

export interface Health {
  state: HealthState;
  key: string;
  params: Record<string, number> | null;
}

export function statusHealth(data: StatusView): Health {
  const creds = Array.isArray(data.credentials) ? data.credentials : [];
  const enabled = creds.filter((c) => !c.disabled).length;
  if (data.list_error) return { state: "error", key: "health.error", params: null };
  if (!data.catalog || !data.catalog.count) return { state: "warn", key: "health.catalog", params: null };
  if (creds.length === 0) return { state: "warn", key: "health.noKeys", params: null };
  return { state: "ok", key: "health.ok", params: { enabled, total: creds.length } };
}

export type ForceState = "On" | "Off" | "Unknown";

export function forceState(config: Partial<PluginConfigView> | null | undefined): ForceState {
  const cfg = config || {};
  if (cfg.force_model_prefix === true) return "On";
  if (cfg.force_model_prefix === false) return "Off";
  return "Unknown";
}

export interface CapView {
  limit: number;
  left: number;
  /** Remaining share of the cap, clamped to 0..1; 0 without a cap. */
  fraction: number;
  /** Extra class for the bar: " bad" under 10 %, " warn" under 30 %. */
  level: "" | " warn" | " bad";
  /** Bar width, e.g. "63.7%". */
  width: string;
}

export function capView(key: Pick<KeyUsageView, "limit_usd" | "limit_remaining_usd">): CapView {
  const limit = Number(key.limit_usd) || 0;
  const left = Number(key.limit_remaining_usd) || 0;
  const fraction = limit > 0 ? Math.max(0, Math.min(1, left / limit)) : 0;
  const level = fraction < 0.1 ? " bad" : fraction < 0.3 ? " warn" : "";
  return { limit, left, fraction, level, width: (fraction * 100).toFixed(1) + "%" };
}

/** Totals for the summary strip of the credentials tab. */
export interface KeySummary {
  /** Balances, once per account. */
  accounts: number;
  available: number;
  reserved: number;
  hasIssuer: boolean;
  /** Provider earnings, once per issuing token; never part of the balance. */
  earners: number;
  earned: number;
  referral: number;
  daily: number;
  weekly: number;
  monthly: number;
  loaded: number;
  pending: number;
  failed: number;
  loading: boolean;
}

// Balance belongs to the account, so keys of the same account are counted
// once; spend is per key and simply adds up.
export function summarizeKeys(creds: CredentialView[], quota: QuotaMap): KeySummary {
  const accounts = new Map<string, { available_usd: number; reserved_usd: number }>();
  const earners = new Map<string, IssuerView>();
  const sum: KeySummary = {
    accounts: 0, available: 0, reserved: 0, hasIssuer: false, earners: 0, earned: 0, referral: 0,
    daily: 0, weekly: 0, monthly: 0, loaded: 0, pending: 0, failed: 0, loading: false,
  };
  for (const cred of creds) {
    if (cred.read_error) continue;
    if (cred.issuer_token_masked) sum.hasIssuer = true;
    const q = quota[cred.auth_index];
    if (q && q.state === "loading") sum.loading = true;
    const d = q && q.data;
    if (!d) {
      if (q && q.state === "error") sum.failed++;
      else if (!cred.disabled) sum.pending++;
      continue;
    }
    sum.loaded++;
    if (d.balance) accounts.set(d.balance.account || "auth:" + cred.auth_index, d.balance);
    if (d.issuer) earners.set(d.issuer.account || "auth:" + cred.auth_index, d.issuer);
    if (d.key) {
      sum.daily += Number(d.key.daily_usd) || 0;
      sum.weekly += Number(d.key.weekly_usd) || 0;
      sum.monthly += Number(d.key.monthly_usd) || 0;
    }
  }
  for (const b of accounts.values()) {
    sum.available += Number(b.available_usd) || 0;
    sum.reserved += Number(b.reserved_usd) || 0;
  }
  for (const e of earners.values()) {
    sum.earned += Number(e.provider_usd) || 0;
    sum.referral += Number(e.referrer_usd) || 0;
  }
  sum.accounts = accounts.size;
  sum.earners = earners.size;
  return sum;
}

/**
 * The keys whose balance to load: enabled keys not loaded yet, or with force
 * every key that is enabled or already showing a balance.
 */
export function quotaQueue(creds: CredentialView[], quota: QuotaMap, force: boolean): string[] {
  return creds
    .filter((c) => !c.read_error && (!c.disabled || (force && quota[c.auth_index])) && (force || !quota[c.auth_index]))
    .map((c) => c.auth_index);
}

/**
 * The line under a credential's name: the file name when a custom label
 * hides it, the masked key and the priority.
 */
export function credSubline(cred: CredentialView, priorityLabel: string): string {
  return [
    cred.label && cred.label !== fileStem(cred.name) ? fileStem(cred.name) : "",
    cred.key_masked,
    cred.priority ? priorityLabel + " " + cred.priority : "",
  ].filter(Boolean).join(" · ");
}

export interface ModelUse {
  keys: number;
  names: Set<string>;
}

/**
 * For each model ID: the names it is registered under in CPA and how many
 * enabled keys serve it.
 */
export function modelUsage(creds: CredentialView[], models: ModelView[]): Map<string, ModelUse> {
  const usage = new Map<string, ModelUse>();
  for (const cred of creds) {
    if (cred.disabled || cred.read_error) continue;
    const ids = cred.all_models ? models.map((m) => m.id) : Array.isArray(cred.models) ? cred.models : [];
    const prefix = cred.effective_prefix || "";
    for (const id of ids) {
      let entry = usage.get(id);
      if (!entry) {
        entry = { keys: 0, names: new Set() };
        usage.set(id, entry);
      }
      entry.keys++;
      entry.names.add(prefix ? prefix + "/" + id : id);
    }
  }
  return usage;
}

/** Catalog models whose id, name or owner contains the filter. */
export function filterModels<M extends { id: string; name?: string; owned_by?: string }>(models: M[], rawFilter: string): M[] {
  const filter = rawFilter.trim().toLowerCase();
  return models.filter((m) => !filter ||
    m.id.toLowerCase().includes(filter) ||
    String(m.name || "").toLowerCase().includes(filter) ||
    String(m.owned_by || "").toLowerCase().includes(filter));
}
