import type { CredentialView, ModelView, StatusView } from "../api/types";
import {
  allCredentials,
  capView,
  credentialsOf,
  credMatches,
  credState,
  credSubline,
  defaultPrefix,
  displayName,
  filterModels,
  forceState,
  isDirect,
  modelUsage,
  egressKey,
  quotaGroups,
  statusHealth,
  summarizeKeys,
  type QuotaMap,
} from "./credentials";

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

function status(over: Partial<StatusView> = {}): StatusView {
  return {
    plugin: "wokey",
    version: "0.1.8",
    now: "2026-11-08T10:00:00Z",
    config: {
      base_url: "https://api.wokey.ai",
      default_prefix: "wokey",
      models_refresh_minutes: 60,
      request_timeout_seconds: 300,
      stream_idle_timeout_seconds: 120,
    },
    catalog: { count: 12 },
    credentials: [cred()],
    ...over,
  };
}

const model = (id: string, over: Partial<ModelView> = {}): ModelView => ({ id, pricing: {}, ...over });

describe("display rules", () => {
  it("names a key by label, then file stem, then masked key", () => {
    expect(displayName(cred({ label: "Team A" }))).toBe("Team A");
    expect(displayName(cred())).toBe("wokey-aaaa");
    expect(displayName(cred({ name: "" }))).toBe("sk-wk-****aaaa");
  });

  it("shows the file name under a custom label only", () => {
    expect(credSubline(cred(), "Priority")).toBe("sk-wk-****aaaa");
    expect(credSubline(cred({ label: "wokey-aaaa" }), "Priority")).toBe("sk-wk-****aaaa");
    expect(credSubline(cred({ label: "Team A", priority: 5 }), "Priority")).toBe("wokey-aaaa · sk-wk-****aaaa · Priority 5");
  });

  it("maps state to badge class and message", () => {
    expect(credState(cred({ read_error: "bad json", disabled: true }))).toEqual(["danger", "state.readError"]);
    expect(credState(cred({ disabled: true, unavailable: true }))).toEqual(["", "state.disabled"]);
    expect(credState(cred({ unavailable: true }))).toEqual(["warn", "state.unavailable"]);
    expect(credState(cred())).toEqual(["ok", "state.enabled"]);
  });

  it("recognises direct proxies", () => {
    expect(isDirect(" Direct ")).toBe(true);
    expect(isDirect("none")).toBe(true);
    expect(isDirect("socks5://h:1080")).toBe(false);
    expect(isDirect(undefined)).toBe(false);
  });

  it("filters keys by label, masked key, file, prefix and note", () => {
    const c = cred({ label: "Team A", note: "cooling down" });
    expect(credMatches(c, "")).toBe(true);
    expect(credMatches(c, "team")).toBe(true);
    expect(credMatches(c, "aaaa")).toBe(true);
    expect(credMatches(c, "wokey")).toBe(true);
    expect(credMatches(c, "cooling")).toBe(true);
    expect(credMatches(c, "zzz")).toBe(false);
  });
});

describe("status helpers", () => {
  it("hides credentials when listing failed, except for log lookups", () => {
    const failed = status({ list_error: "boom" });
    expect(credentialsOf(failed)).toEqual([]);
    expect(allCredentials(failed)).toHaveLength(1);
    expect(credentialsOf(null)).toEqual([]);
    expect(allCredentials(null)).toEqual([]);
  });

  it("uses wokey as the default prefix until the status arrives", () => {
    expect(defaultPrefix(null)).toBe("wokey");
    expect(defaultPrefix(status())).toBe("wokey");
    expect(defaultPrefix(status({ config: { ...status().config, default_prefix: "" } }))).toBe("");
  });

  it("derives the health badge", () => {
    expect(statusHealth(status({ list_error: "x" }))).toEqual({ state: "error", key: "health.error", params: null });
    expect(statusHealth(status({ catalog: { count: 0 } }))).toEqual({ state: "warn", key: "health.catalog", params: null });
    expect(statusHealth(status({ credentials: [] }))).toEqual({ state: "warn", key: "health.noKeys", params: null });
    expect(statusHealth(status({ credentials: [cred(), cred({ auth_index: "2", disabled: true })] }))).toEqual({
      state: "ok",
      key: "health.ok",
      params: { enabled: 1, total: 2 },
    });
  });

  it("reads force-model-prefix", () => {
    expect(forceState({ force_model_prefix: true })).toBe("On");
    expect(forceState({ force_model_prefix: false })).toBe("Off");
    expect(forceState({})).toBe("Unknown");
    expect(forceState(null)).toBe("Unknown");
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

describe("quotaGroups", () => {
  const creds = [
    cred({ auth_index: "1" }),
    cred({ auth_index: "2", disabled: true, proxy_url: "socks5h://u:***@p.example:1080" }),
    cred({ auth_index: "3", disabled: true }),
    cred({ auth_index: "4", read_error: "bad" }),
    cred({ auth_index: "5", proxy_url: "direct" }),
    cred({ auth_index: "6", proxy_url: "socks5h://u:***@p.example:1080" }),
    cred({ auth_index: "7", proxy_url: "NONE" }),
  ];
  const quota: QuotaMap = { "3": { state: "ok" }, "5": { state: "ok" } };

  it("loads keys with no balance yet, disabled ones too, grouped by egress", () => {
    expect(quotaGroups(creds, quota, false)).toEqual([["1"], ["2", "6"], ["7"]]);
  });

  it("with force reloads every readable key", () => {
    expect(quotaGroups(creds, quota, true)).toEqual([["1", "3"], ["2", "6"], ["5", "7"]]);
  });

  it("names the egress a proxy setting picks", () => {
    expect([egressKey(undefined), egressKey(" "), egressKey("Direct"), egressKey("none"), egressKey(" http://h:1 ")])
      .toEqual(["inherit", "inherit", "direct", "direct", "http://h:1"]);
  });
});

describe("models", () => {
  const models = [model("gpt-5", { name: "GPT-5", owned_by: "openai" }), model("claude-sonnet-4-5", { owned_by: "anthropic" }), model("glm-4.6")];

  it("lists the names each model is registered under and how many keys serve it", () => {
    const usage = modelUsage(
      [
        cred({ auth_index: "1", all_models: true, effective_prefix: "wokey" }),
        cred({ auth_index: "2", all_models: false, models: ["gpt-5"], effective_prefix: "" }),
        cred({ auth_index: "3", all_models: false, models: ["gpt-5"], effective_prefix: "wokey" }),
        cred({ auth_index: "4", all_models: true, disabled: true }),
        cred({ auth_index: "5", all_models: true, read_error: "x" }),
      ],
      models,
    );
    expect(usage.get("gpt-5")).toEqual({ keys: 3, names: new Set(["wokey/gpt-5", "gpt-5"]) });
    expect(usage.get("glm-4.6")).toEqual({ keys: 1, names: new Set(["wokey/glm-4.6"]) });
    expect(usage.has("missing")).toBe(false);
  });

  it("filters by id, name and owner", () => {
    expect(filterModels(models, "").map((m) => m.id)).toEqual(["gpt-5", "claude-sonnet-4-5", "glm-4.6"]);
    expect(filterModels(models, "  GPT ").map((m) => m.id)).toEqual(["gpt-5"]);
    expect(filterModels(models, "anthropic").map((m) => m.id)).toEqual(["claude-sonnet-4-5"]);
    expect(filterModels(models, "nothing")).toEqual([]);
  });
});
