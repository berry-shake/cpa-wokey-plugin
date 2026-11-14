// Wokey figures: prices, the spend cap of a key and the totals of the summary strip.

import type { CredentialView } from "@core/api/types";
import type { QuotaMap } from "@core/lib/credentials";
import type { IssuerView, KeyUsageView } from "./types";

/** USD per million tokens; "?" when unknown. */
export function fmtPrice(value: number | null | undefined): string {
  if (value === null || value === undefined) return "?";
  const n = Number(value);
  if (!isFinite(n)) return "?";
  if (n === 0) return "0";
  if (n >= 10) return String(Math.round(n * 100) / 100);
  return String(Number(n.toPrecision(3)));
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
