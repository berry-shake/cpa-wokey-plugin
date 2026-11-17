import { renderToStaticMarkup } from "react-dom/server";
import type { CredentialView, QuotaView } from "@core/api/types";
import { CredCard } from "@core/components/keys/CredCard";
import { makeT } from "@core/i18n";
import { newEditor } from "@core/lib/editor";
import { initialState } from "@core/state/store";
import { components } from "./components";

const t = makeT("en");

function html(markup: string): Document {
  return new DOMParser().parseFromString(markup, "text/html");
}

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

describe("CredCard", () => {
  const data: QuotaView = {
    fetched_at: "2026-11-08T10:00:00Z",
    balance: { available_usd: 12.5, reserved_usd: 0 },
    key: { usage_usd: 0, daily_usd: 1, weekly_usd: 2, monthly_usd: 3, limit_usd: 40, limit_remaining_usd: 10 },
  };

  it("draws the spend cap with its remaining share", () => {
    const doc = html(renderToStaticMarkup(<CredCard t={t} cred={cred} q={{ state: "ok", data }} armedDelete={null} busy={{}} locale="en-US" />));
    const cap = doc.querySelector(".cap");
    expect(cap?.className).toBe("cap warn");
    expect(doc.querySelector(".track")?.getAttribute("aria-valuenow")).toBe("25");
    expect(doc.querySelector(".fill")?.getAttribute("style")).toBe("width:25.0%");
    expect(doc.querySelector(".cap-head strong")?.textContent).toBe(t("usage.capLeft", { left: "$10.00", limit: "$40.00" }));
  });
});

describe("CredCard earnings", () => {
  const issuing = { ...cred, issuer_token_masked: "wkt_****abcd" };

  it("puts the earnings of a key with an issuing token in the corner", () => {
    const data: QuotaView = { fetched_at: "2026-11-08T10:00:00Z", issuer: { account: "p", provider_usd: 7, referrer_usd: 2, provider_frozen: true } };
    const doc = html(renderToStaticMarkup(<CredCard t={t} cred={issuing} q={{ state: "ok", data }} armedDelete={null} busy={{}} locale="en-US" />));
    expect(doc.querySelector(".cred-earn-value")?.textContent).toBe("$7.00");
    expect(doc.querySelector(".cred-earn-value")?.className).toBe("cred-earn-value warn-text");
    expect(doc.querySelector(".cred-earn-note")?.textContent).toBe(t("usage.frozen") + " · " + t("usage.referrer", { v: "$2.00" }));
  });

  it("leaves the corner empty without a token", () => {
    const doc = html(renderToStaticMarkup(<CredCard t={t} cred={cred} q={undefined} armedDelete={null} busy={{}} locale="en-US" />));
    expect(doc.querySelector(".cred-earn")).toBeNull();
  });
});

describe("EditorFields", () => {
  it("shows the issuing token as plain text the browser neither hides nor fills", () => {
    const Fields = components.EditorFields!;
    const doc = html(renderToStaticMarkup(<Fields t={t} s={initialState()} ed={newEditor(1, "add", null, "wokey")} />));
    const issuer = doc.querySelector<HTMLInputElement>("#f-issuer");
    expect(issuer).not.toBeNull();
    expect(issuer!.getAttribute("type")).toBe("text");
    expect(issuer!.getAttribute("autocomplete")).toBe("off");
    expect(issuer!.getAttribute("autocapitalize")).toBe("off");
    expect(doc.querySelector('input[type="password"]')).toBeNull();
  });
});
