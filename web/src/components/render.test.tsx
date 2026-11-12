import { renderToStaticMarkup } from "react-dom/server";
import type { CredentialView, LogEntry, QuotaView } from "../api/types";
import { makeT } from "../i18n";
import { initialState, type State } from "../state/store";
import { CredCard } from "./keys/CredCard";
import { LogRow } from "./logs/LogRow";
import { ProxiesPanel } from "./proxies/ProxiesPanel";

const t = makeT("en");

function html(markup: string): Document {
  return new DOMParser().parseFromString(markup, "text/html");
}

const entry: LogEntry = {
  id: "3",
  request_id: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
  time: "2026-11-08T10:02:11Z",
  endpoint: "/v1/messages",
  model: "wokey/claude-sonnet-4-5",
  upstream_model: "claude-sonnet-4-5",
  stream: true,
  status: "success",
  http_status: 200,
  credential: "sk-wk-****aaaa",
  auth_id: "wokey-aaaa.json",
  duration_ms: 8421,
  ttft_ms: 1320,
  input_tokens: 48211,
  output_tokens: 1840,
  cached_tokens: 40960,
};

const cred: CredentialView = {
  auth_index: "7",
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
  success: 3,
  failed: 1,
};

describe("LogRow", () => {
  const value = (doc: Document, label: string) =>
    [...doc.querySelectorAll(".log-detail .datum")].find((n) => n.querySelector(".datum-label")?.textContent === label)?.querySelector(".datum-value")?.textContent;

  it("shows the full request ID first in the details", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={entry} creds={[cred]} locale="en-US" />));
    expect(doc.querySelector(".log-detail-head")?.textContent).toBe(t("logs.d.title"));
    const first = doc.querySelector(".log-detail-grid > div");
    expect(first?.className).toBe("datum reqid wide");
    expect(first?.querySelector(".datum-label")?.textContent).toBe(t("logs.d.reqid"));
    expect(first?.querySelector(".datum-value")?.textContent).toBe("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b");
    expect(first?.getAttribute("title")).toBeNull();
  });

  it("looks up the auth index and names the credential by file", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={entry} creds={[cred]} locale="en-US" />));
    const clip = doc.querySelector("summary .clip");
    expect(clip?.textContent).toBe("wokey-aaaa");
    expect(clip?.getAttribute("title")).toBe("sk-wk-****aaaa");
    expect(value(doc, t("logs.d.auth"))).toBe("7");
    expect(value(doc, t("logs.d.cred"))).toBe("wokey-aaaa");
    expect(value(doc, t("logs.d.key"))).toBe("sk-wk-****aaaa");
  });

  it("lists models, result, timing and usage in full", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={{ ...entry, reasoning_effort: "high", cache_write_tokens: 1514 }} creds={[cred]} locale="en-US" />));
    expect(value(doc, t("logs.d.model"))).toBe("wokey/claude-sonnet-4-5");
    expect(value(doc, t("logs.d.upstream"))).toBe("claude-sonnet-4-5");
    expect(value(doc, t("logs.d.served"))).toBe(t("logs.notReported"));
    expect(value(doc, t("logs.d.effort"))).toBe("high");
    expect(value(doc, t("logs.d.result"))).toBe("HTTP 200 · " + t("logs.stream"));
    expect(value(doc, t("logs.d.ttft"))).toBe("1.3s");
    expect(value(doc, t("logs.d.total"))).toBe("8.4s");
    expect(value(doc, t("logs.d.input"))).toBe((48211).toLocaleString());
    expect(value(doc, t("logs.d.cacheRead"))).toBe((40960).toLocaleString() + " (85.0%)");
    expect(value(doc, t("logs.d.cacheWrite"))).toBe((1514).toLocaleString());
    expect(doc.querySelector(".log-detail-error")).toBeNull();
  });

  it("shows the key, not the label older builds stored, and the label beside the file name", () => {
    const labelled = { ...cred, label: "temptinglemon" };
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={{ ...entry, credential: "temptinglemon" }} creds={[labelled]} locale="en-US" />));
    expect(value(doc, t("logs.d.key"))).toBe("sk-wk-****aaaa");
    expect(doc.querySelector(".datum-cred .datum-note")?.textContent).toBe("temptinglemon");
  });

  it("puts the error under the grid", () => {
    const failed: LogEntry = { ...entry, status: "error", http_status: 429, error_code: "rate_limit_exceeded", error_phase: "request", error: "slow down" };
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={failed} creds={[cred]} locale="en-US" />));
    expect(value(doc, t("logs.d.code"))).toBe("rate_limit_exceeded");
    expect(value(doc, t("logs.d.phase"))).toBe(t("logs.phase.request"));
    expect(doc.querySelector(".datum-result .danger-text")?.textContent).toBe("HTTP 429");
    expect(doc.querySelector(".log-detail-error .danger-text")?.textContent).toBe("slow down");
  });

  it("shows the result as a tag with the stream mode below", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={entry} creds={[cred]} locale="en-US" />));
    const result = doc.querySelector("summary .log-result");
    expect(result?.querySelector(".badge")?.className).toBe("badge tag ok");
    expect(result?.querySelector(".badge")?.textContent).toBe("HTTP 200");
    expect(result?.querySelector(".sub")?.textContent).toBe(t("logs.stream"));
    expect(doc.querySelector(".log-attempts")).toBeNull();
  });

  it("shows what 0.1.9 records", () => {
    const full: LogEntry = {
      ...entry,
      client_path: "/v1/messages?beta=true",
      user_agent: "claude-cli/2.1.3 (external, cli)",
      auth_index: "c0ffee",
      egress: "socks5://proxy.example:1080",
      upstream_status: 200,
      upstream_request_id: "req_01ABC",
      finish_reason: "end_turn",
      frames: 212,
      last_frame_ms: 8390,
      usage_reported: true,
    };
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={full} creds={[]} locale="en-US" />));
    expect(value(doc, t("logs.d.start"))).toBe(new Date("2026-11-08T10:02:11Z").toLocaleString("en-US"));
    expect(value(doc, t("logs.d.end"))).toBe(new Date("2026-11-08T10:02:19.421Z").toLocaleString("en-US"));
    expect(value(doc, t("logs.d.client"))).toBe("claude-cli/2.1.3 (external, cli)");
    expect(value(doc, t("logs.d.clientPath"))).toBe("/v1/messages?beta=true");
    expect(value(doc, t("logs.d.auth"))).toBe("c0ffee");
    expect(value(doc, t("logs.d.upstreamStatus"))).toBe("HTTP 200");
    expect(value(doc, t("logs.d.finish"))).toBe(t("logs.finish.stop") + "end_turn");
    expect(doc.querySelector(".datum-finish .datum-note")?.textContent).toBe("end_turn");
    expect(value(doc, t("logs.d.egress"))).toBe("socks5://proxy.example:1080");
    expect(value(doc, t("logs.d.upstreamReqid"))).toBe("req_01ABC");
    expect(value(doc, t("logs.d.frames"))).toBe("212" + t("logs.lastFrame", { t: "8.4s" }));
    const labels = [...doc.querySelectorAll(".log-detail-grid .datum-label")].map((n) => n.textContent);
    expect(labels.slice(0, 6)).toEqual(["reqid", "start", "end", "client", "clientPath", "endpoint"].map((k) => t("logs.d." + k)));
  });

  it("leaves the newer fields blank on older entries", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={entry} creds={[cred]} locale="en-US" />));
    for (const k of ["client", "clientPath", "upstreamStatus", "finish", "egress", "upstreamReqid", "frames"]) expect(value(doc, t("logs.d." + k))).toBe("—");
  });

  it("names the egress and tells a missing response", () => {
    const direct = html(renderToStaticMarkup(<LogRow t={t} e={{ ...entry, egress: "direct" }} creds={[]} locale="en-US" />));
    expect(value(direct, t("logs.d.egress"))).toBe(t("proxy.direct"));
    expect(value(direct, t("logs.d.upstreamStatus"))).toBe(t("logs.noResponse"));
    const env = html(renderToStaticMarkup(<LogRow t={t} e={{ ...entry, egress: "env", upstream_status: 529, http_status: 503, status: "error" }} creds={[]} locale="en-US" />));
    expect(value(env, t("logs.d.egress"))).toBe(t("logs.egress.env"));
    expect(env.querySelector(".datum-upstatus .danger-text")?.textContent).toBe("HTTP 529");
  });

  it("says so when the response carried no usage", () => {
    const e: LogEntry = { ...entry, input_tokens: 0, output_tokens: 0, cached_tokens: 0, upstream_status: 200, usage_reported: false };
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={e} creds={[]} locale="en-US" />));
    expect(doc.querySelector("summary .log-usage")?.firstElementChild?.textContent).toBe(t("logs.notReported"));
    for (const k of ["input", "output", "reasoning", "cacheRead", "cacheWrite"]) expect(value(doc, t("logs.d." + k))).toBe(t("logs.notReported"));
    expect(value(doc, t("logs.d.finish"))).toBe(t("logs.notReported"));
  });

  it("shows the retry wait of a 429", () => {
    const e: LogEntry = { ...entry, status: "error", http_status: 429, upstream_status: 429, retry_after: 3, error: "slow down" };
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={e} creds={[]} locale="en-US" />));
    expect(value(doc, t("logs.d.retryAfter"))).toBe(t("logs.seconds", { n: 3 }));
    expect(value(doc, t("logs.d.finish"))).toBe("—");
  });

  it("marks requests CPA tried more than once", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={{ ...entry, attempts: 3 }} creds={[cred]} locale="en-US" />));
    const sub = doc.querySelector("summary .log-result .sub");
    expect(sub?.textContent).toBe(t("logs.stream") + " · ×3");
    expect(sub?.getAttribute("title")).toBe(t("logs.attemptsHint", { n: 3 }));
    expect(doc.querySelector(".log-attempts-head")?.textContent).toBe(t("logs.attempts", { n: 3 }));
    expect(doc.querySelector(".log-attempts .sub")?.textContent).toBe(t("logs.attemptsLoading"));
  });

  it("shows a dash without a request ID", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={{ ...entry, request_id: "" }} creds={[]} locale="en-US" />));
    expect(doc.querySelector(".reqid .datum-value")?.textContent).toBe("—");
  });
});

describe("CredCard", () => {
  const data: QuotaView = {
    fetched_at: "2026-11-08T10:00:00Z",
    balance: { available_usd: 12.5, reserved_usd: 0 },
    key: { usage_usd: 0, daily_usd: 1, weekly_usd: 2, monthly_usd: 3, limit_usd: 40, limit_remaining_usd: 10 },
  };

  it("draws the spend cap with its remaining share", () => {
    const doc = html(renderToStaticMarkup(<CredCard t={t} cred={cred} q={{ state: "ok", data }} armed={false} busy={{}} locale="en-US" />));
    const cap = doc.querySelector(".cap");
    expect(cap?.className).toBe("cap warn");
    expect(doc.querySelector(".track")?.getAttribute("aria-valuenow")).toBe("25");
    expect(doc.querySelector(".fill")?.getAttribute("style")).toBe("width:25.0%");
    expect(doc.querySelector(".cap-head strong")?.textContent).toBe(t("usage.capLeft", { left: "$10.00", limit: "$40.00" }));
  });

  it("arms delete and offers a balance check", () => {
    const doc = html(renderToStaticMarkup(<CredCard t={t} cred={cred} q={undefined} armed busy={{}} locale="en-US" />));
    expect(doc.querySelector(".btn-danger")?.className).toContain("armed");
    expect(doc.querySelector(".btn-danger")?.textContent).toBe(t("row.confirmDelete"));
    expect(doc.querySelector(".usage-state span")?.textContent).toBe(t("usage.notLoaded"));
  });
});

describe("CredCard proxy", () => {
  const proxyValue = (doc: Document) =>
    [...doc.querySelectorAll(".facts .meter")].find((n) => n.querySelector(".meter-label")?.textContent === t("card.proxy"))?.querySelector(".meter-value");

  it("names a list proxy and keeps the address in the tooltip", () => {
    const listed = { ...cred, proxy_url: "socks5://u:xxxxx@hk:1080", proxy_id: "p1", proxy_name: "HK" };
    const value = proxyValue(html(renderToStaticMarkup(<CredCard t={t} cred={listed} q={undefined} armed={false} busy={{}} locale="en-US" />)));
    expect(value?.textContent).toBe("HK");
    expect(value?.getAttribute("title")).toBe("socks5://u:xxxxx@hk:1080");
  });

  it("shows a custom address, direct or inherit as before", () => {
    const custom = proxyValue(html(renderToStaticMarkup(<CredCard t={t} cred={{ ...cred, proxy_url: "http://x:1" }} q={undefined} armed={false} busy={{}} locale="en-US" />)));
    expect(custom?.textContent).toBe("http://x:1");
    const direct = proxyValue(html(renderToStaticMarkup(<CredCard t={t} cred={{ ...cred, proxy_url: "none" }} q={undefined} armed={false} busy={{}} locale="en-US" />)));
    expect(direct?.textContent).toBe(t("proxy.direct"));
    const inherit = proxyValue(html(renderToStaticMarkup(<CredCard t={t} cred={cred} q={undefined} armed={false} busy={{}} locale="en-US" />)));
    expect(inherit?.textContent).toBe(t("proxy.inherit"));
  });
});

describe("ProxiesPanel", () => {
  const base = (over: Partial<State> = {}): State => ({
    ...initialState(),
    lang: "en",
    tab: "proxies",
    view: "app",
    status: {
      plugin: "wokey", version: "t", now: "", catalog: { count: 0 },
      config: { base_url: "", default_prefix: "wokey", models_refresh_minutes: 0, request_timeout_seconds: 0, stream_idle_timeout_seconds: 0 },
      credentials: [
        cred,
        { ...cred, auth_index: "8", name: "b.json", proxy_url: "direct" },
        { ...cred, auth_index: "9", name: "c.json", proxy_url: "socks5://u:xxxxx@hk:1080", proxy_id: "p1", proxy_name: "HK" },
      ],
    },
    proxies: { proxies: [{ id: "p1", name: "HK", url: "socks5://u:xxxxx@hk:1080", label: "socks5://hk:1080" }] },
    ...over,
  });
  const rows = (doc: Document) => [...doc.querySelectorAll("#panel-proxies tbody tr")];
  const cells = (row: Element) => [...row.querySelectorAll("td")].map((td) => td.textContent);

  it("lists the built-in rows first and leaves exit, latency and time blank until checked", () => {
    const doc = html(renderToStaticMarkup(<ProxiesPanel t={t} s={base()} />));
    expect([...doc.querySelectorAll("#panel-proxies thead th")].map((th) => th.textContent)).toContain(t("proxies.col.checked"));
    const all = rows(doc);
    expect(all.map((r) => r.getAttribute("data-key"))).toEqual(["inherit", "direct", "id:p1"]);
    expect(cells(all[0]).slice(0, 6)).toEqual([t("proxies.inherit") + t("proxies.builtin"), t("proxies.inheritNone"), t("proxies.usedBy", { n: 1 }), "—", "—", "—"]);
    expect(cells(all[1]).slice(2, 6)).toEqual([t("proxies.usedBy", { n: 1 }), "—", "—", "—"]);
    expect(cells(all[2]).slice(0, 6)).toEqual(["HK", "socks5://u:xxxxx@hk:1080", t("proxies.usedBy", { n: 1 }), "—", "—", "—"]);
    // Built-in rows can only be checked.
    expect(all[0].querySelectorAll("button").length).toBe(1);
    expect(all[2].querySelectorAll("button").length).toBe(3);
    expect(doc.querySelector("#proxies-import")).toBeNull();
  });

  it("shows saved results with flag and time, progress and failures", () => {
    const s = base({
      proxies: {
        proxies: [{
          id: "p1", name: "HK", url: "socks5://u:xxxxx@hk:1080", label: "socks5://hk:1080",
          last_check: { ok: true, egress: "socks5://hk:1080", ip: "203.0.113.7", country: "HK", city: "Kwai Chung", latency_ms: 412.4, checked_at: "2026-11-12T14:00:00Z" },
        }],
        // A saved result stays hidden behind a check in flight.
        builtin_checks: { direct: { ok: true, egress: "direct", ip: "45.32.10.8", country: "SG", checked_at: "2026-11-12T13:00:00Z" } },
      },
      proxyChecks: {
        direct: { state: "loading", url: "" },
        inherit: { state: "error", url: "", error: "dial tcp: timeout" },
      },
    });
    const doc = html(renderToStaticMarkup(<ProxiesPanel t={t} s={s} />));
    const [inherit, direct, hk] = rows(doc);
    expect(hk.querySelector(".proxy-egress .mono")?.textContent).toBe("203.0.113.7");
    expect(hk.querySelector(".proxy-flag")?.getAttribute("src")).toBe("/v0/resource/plugins/wokey/flag.svg?cc=hk");
    expect(hk.querySelector(".proxy-egress .sub")?.textContent).toBe("Hong Kong · Kwai Chung");
    expect(cells(hk)[4]).toBe(t("proxies.latency", { n: 412 }));
    expect(cells(hk)[5]).not.toBe("—");
    expect(cells(direct)[5]).toBe("…");
    expect(cells(direct)[3]).toBe(t("proxies.checking"));
    expect(direct.querySelector("button")?.hasAttribute("disabled")).toBe(true);
    const failed = inherit.querySelector(".proxy-egress .danger-text");
    expect(failed?.textContent).toBe(t("proxies.failed"));
    const reason = inherit.querySelector(".proxy-egress .proxy-error");
    expect(reason?.textContent).toBe("dial tcp: timeout");
    expect(reason?.getAttribute("title")).toBe("dial tcp: timeout");
    expect(cells(inherit)[4]).toBe("—");
  });

  it("offers to import addresses the list lacks and explains an armed delete", () => {
    const s = base({ armedDelete: "proxy:p1" });
    s.status!.credentials.push({ ...cred, auth_index: "10", name: "d.json", proxy_url: "http://other:1" });
    const doc = html(renderToStaticMarkup(<ProxiesPanel t={t} s={s} />));
    expect(doc.querySelector("#proxies-import")?.textContent).toBe(t("proxies.import"));
    const hk = rows(doc)[2];
    expect(hk.querySelector(".btn-danger")?.textContent).toBe(t("row.confirmDelete"));
    expect(hk.querySelector(".proxy-delete-note")?.textContent).toBe(t("proxies.deleteUsedNote", { n: 1 }));
  });

  it("shows the global proxy and an empty list", () => {
    const doc = html(renderToStaticMarkup(<ProxiesPanel t={t} s={base({ proxies: { proxies: [], global_proxy: "http://g:3128" } })} />));
    const all = rows(doc);
    expect(cells(all[0])[1]).toBe("http://g:3128");
    expect(all[2].textContent).toBe(t("proxies.empty"));
  });
});
