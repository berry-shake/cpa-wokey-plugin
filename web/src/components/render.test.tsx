import { renderToStaticMarkup } from "react-dom/server";
import type { CredentialView, LogEntry, QuotaView } from "../api/types";
import { makeT } from "../i18n";
import { CredCard } from "./keys/CredCard";
import { LogRow } from "./logs/LogRow";

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
  it("shows the full request ID first in the details", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={entry} creds={[cred]} locale="en-US" />));
    const first = doc.querySelector(".log-detail > div");
    expect(first?.className).toBe("reqid");
    expect(first?.querySelector(".sub")?.textContent).toBe(t("logs.d.reqid"));
    expect(first?.querySelector(".mono")?.textContent).toBe("0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b");
    expect(first?.getAttribute("title")).toBeNull();
  });

  it("looks up the auth index and names the credential by file", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={entry} creds={[cred]} locale="en-US" />));
    const clip = doc.querySelector("summary .clip");
    expect(clip?.textContent).toBe("wokey-aaaa");
    expect(clip?.getAttribute("title")).toBe("sk-wk-****aaaa");
    const values = [...doc.querySelectorAll(".log-detail .mono")].map((n) => n.textContent);
    expect(values).toContain("7");
  });

  it("shows a dash without a request ID", () => {
    const doc = html(renderToStaticMarkup(<LogRow t={t} e={{ ...entry, request_id: "" }} creds={[]} locale="en-US" />));
    expect(doc.querySelector(".reqid .mono")?.textContent).toBe("—");
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
