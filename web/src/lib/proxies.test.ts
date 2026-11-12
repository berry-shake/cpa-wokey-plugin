import type { CredentialView, ProxyView } from "../api/types";
import {
  buildProxyBody, checkedAtText, checkTarget, checkUrl, countryName, DIRECT_KEY, egressName, egressPlace, flagUrl, INHERIT_KEY,
  newProxyDialog, proxyCheckKey, proxyUsage, pruneChecks, runLimited, savedCheck, shownCheck, unlistedProxyKeys, withSavedCheck,
  type ProxyCheckMap,
} from "./proxies";

function cred(over: Partial<CredentialView> = {}): CredentialView {
  return {
    auth_index: "1",
    id: "wokey-a.json",
    name: "wokey-a.json",
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

const hk: ProxyView = { id: "p1", name: "HK", url: "socks5://u:xxxxx@hk:1080", label: "socks5://hk:1080" };
const jp: ProxyView = { id: "p2", name: "jp:8080", url: "http://jp:8080", label: "http://jp:8080" };

describe("proxyUsage", () => {
  it("counts keys per list proxy and per built-in choice", () => {
    const usage = proxyUsage([
      cred(),
      cred({ proxy_url: "direct" }),
      cred({ proxy_url: "NONE" }),
      cred({ proxy_url: hk.url, proxy_id: "p1" }),
      cred({ proxy_url: hk.url, proxy_id: "p1" }),
      cred({ proxy_url: "http://other:1" }),
      cred({ read_error: "boom" }),
    ]);
    expect(usage.inherit).toBe(1);
    expect(usage.direct).toBe(2);
    expect(usage.custom).toBe(1);
    expect(usage.byId.get("p1")).toBe(2);
    expect(usage.byId.get("p2")).toBeUndefined();
  });
});

describe("unlistedProxyKeys", () => {
  it("finds keys with an address the list lacks", () => {
    const custom = cred({ name: "c", proxy_url: "http://other:1" });
    const keys = unlistedProxyKeys([
      cred(),
      cred({ proxy_url: " direct " }),
      cred({ proxy_url: hk.url, proxy_id: "p1" }),
      custom,
      cred({ proxy_url: "http://x:1", read_error: "boom" }),
    ]);
    expect(keys).toEqual([custom]);
  });
});

describe("check keys", () => {
  it("maps keys to request bodies", () => {
    expect(proxyCheckKey("p1")).toBe("id:p1");
    expect(checkTarget("id:p1")).toEqual({ id: "p1" });
    expect(checkTarget(DIRECT_KEY)).toEqual({ target: "direct" });
    expect(checkTarget(INHERIT_KEY)).toEqual({ target: "inherit" });
  });

  it("resolves the address a key points at", () => {
    expect(checkUrl("id:p1", [hk], undefined)).toBe(hk.url);
    expect(checkUrl("id:p9", [hk], undefined)).toBeNull();
    expect(checkUrl(DIRECT_KEY, [], undefined)).toBe("");
    expect(checkUrl(INHERIT_KEY, [], "http://g:1")).toBe("http://g:1");
    expect(checkUrl(INHERIT_KEY, [], undefined)).toBe("");
  });

  it("drops results of deleted proxies and changed addresses", () => {
    const checks: ProxyCheckMap = {
      "id:p1": { state: "ok", url: hk.url, result: { ok: true, ip: "1.1.1.1" } },
      "id:p2": { state: "ok", url: "http://jp-old:8080", result: { ok: true, ip: "2.2.2.2" } },
      "id:p3": { state: "error", url: "http://gone:1", error: "x" },
      direct: { state: "ok", url: "", result: { ok: true, ip: "3.3.3.3" } },
      inherit: { state: "ok", url: "http://g-old:1", result: { ok: true, ip: "4.4.4.4" } },
    };
    expect(Object.keys(pruneChecks(checks, [hk, jp], "http://g:1")).sort()).toEqual(["direct", "id:p1"]);
    expect(Object.keys(pruneChecks(checks, [hk, jp], "http://g-old:1")).sort()).toEqual(["direct", "id:p1", "inherit"]);
  });
});

describe("egress text", () => {
  it("joins the country name and city, skipping blanks", () => {
    expect(egressPlace({ country: "HK", region: "Kwai Tsing", city: "Kwai Chung" }, "en-US")).toBe("Hong Kong · Kwai Chung");
    expect(egressPlace({ country: "jp", region: "Tokyo", city: "" }, "en-US")).toBe("Japan · Tokyo");
    expect(egressPlace({ country: "Germany", city: "Berlin" }, "en-US")).toBe("Germany · Berlin");
    expect(egressPlace({}, "en-US")).toBe("");
    expect(egressPlace(undefined, "en-US")).toBe("");
  });

  it("names countries in the page language", () => {
    expect(countryName("HK", "zh-CN")).toBe("香港");
    expect(countryName("TW", "en-US")).toBe("Taiwan");
    expect(countryName("", "en-US")).toBe("");
  });

  it("links a flag only for two-letter codes", () => {
    expect(flagUrl("HK")).toBe("/v0/resource/plugins/wokey/flag.svg?cc=hk");
    expect(flagUrl("Germany")).toBe("");
    expect(flagUrl(undefined)).toBe("");
  });

  it("shows the time of today's checks and the date of older ones", () => {
    const now = new Date(2026, 10, 12, 22, 30);
    expect(checkedAtText(new Date(2026, 10, 12, 9, 5, 7).toISOString(), "en-US", now)).toBe("09:05:07");
    expect(checkedAtText(new Date(2026, 10, 11, 21, 4).toISOString(), "en-US", now)).toMatch(/^11\/11,? 21:04$/);
    expect(checkedAtText(new Date(2025, 0, 2, 3, 4).toISOString(), "en-US", now)).toContain("2025");
  });

  it("names the egress a check went through", () => {
    expect(egressName("direct")).toEqual({ key: "proxy.direct", params: null });
    expect(egressName("env")).toEqual({ key: "logs.egress.env", params: null });
    expect(egressName("socks5://hk:1080")).toEqual({ key: "proxies.via", params: { egress: "socks5://hk:1080" } });
    expect(egressName(undefined)).toBeNull();
  });
});

describe("runLimited", () => {
  it("runs every item with at most limit in flight", async () => {
    let running = 0;
    let peak = 0;
    const done: number[] = [];
    await runLimited([1, 2, 3, 4, 5, 6, 7, 8, 9], 4, async (n) => {
      running++;
      peak = Math.max(peak, running);
      await new Promise((resolve) => setTimeout(resolve, 5));
      running--;
      done.push(n);
    });
    expect(peak).toBe(4);
    expect(done.sort((a, b) => a - b)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9]);
  });

  it("keeps going after a failure and handles no items", async () => {
    const done: number[] = [];
    await runLimited([1, 2, 3], 2, async (n) => {
      if (n === 1) throw new Error("boom");
      done.push(n);
    });
    expect(done.sort()).toEqual([2, 3]);
    await expect(runLimited([], 4, async () => {})).resolves.toBeUndefined();
  });
});

describe("proxy dialog", () => {
  it("needs an address when adding", () => {
    expect(buildProxyBody(newProxyDialog(1, null))).toEqual({ error: "proxies.dialog.needUrl" });
    const d = { ...newProxyDialog(1, null), name: " HK ", url: " socks5://u:p@hk:1080 " };
    expect(buildProxyBody(d)).toEqual({ body: { name: "HK", url: "socks5://u:p@hk:1080" }, changed: true, urlChanged: true });
  });

  it("starts an edit with an empty address and the saved one as placeholder", () => {
    const d = newProxyDialog(2, hk, 3);
    expect(d).toMatchObject({ mode: "edit", id: "p1", title: "HK", name: "HK", url: "", origUrl: hk.url, usedBy: 3 });
    expect(buildProxyBody(d)).toEqual({ body: { id: "p1" }, changed: false, urlChanged: false });
  });

  it("sends only what changed when editing", () => {
    const d = newProxyDialog(2, hk, 0);
    expect(buildProxyBody({ ...d, name: "Hong Kong" })).toEqual({ body: { id: "p1", name: "Hong Kong" }, changed: true, urlChanged: false });
    expect(buildProxyBody({ ...d, url: "socks5://hk2:1080" })).toEqual({ body: { id: "p1", url: "socks5://hk2:1080" }, changed: true, urlChanged: true });
    expect(buildProxyBody({ ...d, name: "" })).toEqual({ body: { id: "p1", name: "" }, changed: true, urlChanged: false });
  });
});

describe("saved checks", () => {
  const data = {
    proxies: [
      { id: "p1", name: "HK", url: "socks5://hk:1080", label: "socks5://hk:1080", last_check: { ok: true, ip: "1.1.1.1", checked_at: "t1" } },
      { id: "p2", name: "JP", url: "http://jp:8080", label: "http://jp:8080" },
    ],
    builtin_checks: { direct: { ok: false, error: "boom", checked_at: "t2" } },
  };

  it("reads a row's saved check", () => {
    expect(savedCheck("id:p1", data)?.ip).toBe("1.1.1.1");
    expect(savedCheck("id:p2", data)).toBeUndefined();
    expect(savedCheck(DIRECT_KEY, data)?.error).toBe("boom");
    expect(savedCheck(INHERIT_KEY, data)).toBeUndefined();
    expect(savedCheck("id:p1", null)).toBeUndefined();
  });

  it("prefers a check in flight over the saved one", () => {
    expect(shownCheck("id:p1", {}, data)?.state).toBe("ok");
    expect(shownCheck(DIRECT_KEY, {}, data)).toMatchObject({ state: "error", error: "boom" });
    expect(shownCheck("id:p1", { "id:p1": { state: "loading", url: "socks5://hk:1080" } }, data)?.state).toBe("loading");
    expect(shownCheck("id:p2", {}, data)).toBeUndefined();
  });

  it("stores a finished check on its row", () => {
    const next = withSavedCheck(data, "id:p2", { ok: true, ip: "2.2.2.2" });
    expect(next.proxies[1].last_check?.ip).toBe("2.2.2.2");
    expect(next.proxies[0].last_check?.ip).toBe("1.1.1.1");
    expect(data.proxies[1].last_check).toBeUndefined();
    const inherit = withSavedCheck(data, INHERIT_KEY, { ok: true, ip: "3.3.3.3" });
    expect(inherit.builtin_checks?.inherit?.ip).toBe("3.3.3.3");
    expect(inherit.builtin_checks?.direct?.error).toBe("boom");
  });
});
