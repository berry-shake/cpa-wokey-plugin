import type { CredentialView } from "@core/api/types";
import type { QuotaMap } from "@core/lib/credentials";
import { capView, fmtPrice, summarizeKeys } from "./lib";

function cred(over: Partial<CredentialView> = {}): CredentialView {
  return {
    auth_index: "1",
    id: "wokey-aaaa.json",
    name: "wokey-aaaa.json",
    label: "",
    key_masked: "sk-wk-****aaaa",
    disabled: false,
    unavailable: false,
    priority: 0,
    prefix: null,
    effective_prefix: "wokey",
    all_models: true,
    models: null,
    registered_models: 0,
    registered_known: false,
    success: 0,
    failed: 0,
    ...over,
  };
}

describe("fmtPrice", () => {
  it.each([
    [undefined, "?"],
    [null, "?"],
    [0, "0"],
    [15, "15"],
    [12.3456, "12.35"],
    [3, "3"],
    [0.075, "0.075"],
    [1.23456, "1.23"],
  ])("%s -> %s", (value, want) => {
    expect(fmtPrice(value as number | null | undefined)).toBe(want);
  });
});

describe("capView", () => {
  it("computes the remaining share and its level", () => {
    const cap = capView({ limit_usd: 100, limit_remaining_usd: 63.7 });
    expect(cap).toMatchObject({ limit: 100, left: 63.7, level: "", width: "63.7%" });
    expect(cap.fraction).toBeCloseTo(0.637);
    expect(capView({ limit_usd: 100, limit_remaining_usd: 29.99 }).level).toBe(" warn");
    expect(capView({ limit_usd: 100, limit_remaining_usd: 30 }).level).toBe("");
    expect(capView({ limit_usd: 100, limit_remaining_usd: 9.9 }).level).toBe(" bad");
    expect(capView({ limit_usd: 100, limit_remaining_usd: 10 }).level).toBe(" warn");
  });

  it("clamps to the bar", () => {
    expect(capView({ limit_usd: 10, limit_remaining_usd: 25 })).toMatchObject({ fraction: 1, width: "100.0%" });
    expect(capView({ limit_usd: 10, limit_remaining_usd: -2 })).toMatchObject({ fraction: 0, width: "0.0%", level: " bad" });
    expect(capView({ limit_usd: 0, limit_remaining_usd: 5 })).toMatchObject({ fraction: 0, width: "0.0%" });
    expect(capView({})).toMatchObject({ limit: 0, left: 0, fraction: 0 });
  });
});

describe("summarizeKeys", () => {
  const creds = [
    cred({ auth_index: "1" }),
    cred({ auth_index: "2" }),
    cred({ auth_index: "3", issuer_token_masked: "wkt_****" }),
    cred({ auth_index: "4", disabled: true }),
    cred({ auth_index: "5" }),
    cred({ auth_index: "6", read_error: "bad" }),
  ];
  const at = "2026-11-08T10:00:00Z";

  it("counts each account's balance once and adds spend per key", () => {
    const quota: QuotaMap = {
      "1": { state: "ok", data: { fetched_at: at, balance: { account: "acc", available_usd: 10, reserved_usd: 1 }, key: { usage_usd: 0, daily_usd: 1, weekly_usd: 2, monthly_usd: 3 } } },
      "2": { state: "ok", data: { fetched_at: at, balance: { account: "acc", available_usd: 10, reserved_usd: 1 }, key: { usage_usd: 0, daily_usd: 0.5, weekly_usd: 1, monthly_usd: 1.5 } } },
      "3": { state: "loading", data: { fetched_at: at, balance: { account: "other", available_usd: 5, reserved_usd: 0 }, issuer: { account: "p", provider_usd: 7, referrer_usd: 2 } } },
      "5": { state: "error", error: "timeout" },
      "6": { state: "ok", data: { fetched_at: at, balance: { account: "ignored", available_usd: 99, reserved_usd: 0 } } },
    };
    const sum = summarizeKeys(creds, quota);
    expect(sum).toMatchObject({
      accounts: 2,
      available: 15,
      reserved: 1,
      hasIssuer: true,
      earners: 1,
      earned: 7,
      referral: 2,
      daily: 1.5,
      weekly: 3,
      monthly: 4.5,
      loaded: 3,
      pending: 0,
      failed: 1,
      loading: true,
    });
  });

  it("counts enabled keys without data as pending", () => {
    const sum = summarizeKeys(creds, {});
    expect(sum).toMatchObject({ accounts: 0, loaded: 0, pending: 4, failed: 0, loading: false, hasIssuer: true });
  });

  it("keys without an account are counted by auth index", () => {
    const quota: QuotaMap = {
      "1": { state: "ok", data: { fetched_at: at, balance: { available_usd: 1, reserved_usd: 0 } } },
      "2": { state: "ok", data: { fetched_at: at, balance: { available_usd: 2, reserved_usd: 0 } } },
    };
    expect(summarizeKeys(creds.slice(0, 2), quota)).toMatchObject({ accounts: 2, available: 3 });
  });
});
