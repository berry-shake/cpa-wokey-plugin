import type { ProviderPlugin } from "@core/contract";
import { fmtTokens } from "@core/lib/format";
import { fmtPrice } from "./lib";

export const plugin: ProviderPlugin = {
  id: "wokey",
  name: "Wokey",
  repo: "https://github.com/berry-shake/cpa-wokey-plugin",
  models: {
    matches: (m, filter) =>
      String(m.name || "").toLowerCase().includes(filter) ||
      String(m.owned_by || "").toLowerCase().includes(filter),
  },
  editor: {
    init: (cred) => ({
      issuer: "",
      issuerClear: false,
      issuerPlaceholder: (cred && cred.issuer_token_masked) || "wkt_…",
      issuerSaved: (cred && cred.issuer_token_masked) || "",
    }),
    // The saved token is never sent to the page: empty keeps it, the checkbox
    // removes it.
    build(ed, body, adding) {
      const issuer = ed.issuerClear ? "" : ed.issuer.trim();
      if (issuer) {
        if (!/^wkt_\S+$/.test(issuer)) return "form.badIssuer";
        body.issuer_token = issuer;
      } else if (!adding && ed.issuerClear) {
        body.issuer_token = "";
      }
      return undefined;
    },
    clearSecrets: (ed) => (ed.issuer ? { issuer: "" } : null),
    // A new token changes the key's earnings.
    reloadsQuota: (body) => body.issuer_token !== undefined,
    pickName: (_t, m) => ({ text: m.name || "", title: m.name || "" }),
    modelMeta(t, m) {
      const parts: string[] = [];
      if (m.context_length) parts.push(t("picker.ctx", { ctx: fmtTokens(m.context_length) }));
      const price = m.pricing || {};
      if (price.input !== undefined || price.output !== undefined) {
        parts.push(t("picker.price", { input: fmtPrice(price.input), output: fmtPrice(price.output) }));
      }
      return parts.join(" · ");
    },
  },
};
