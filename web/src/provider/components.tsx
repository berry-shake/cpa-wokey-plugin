import type { ReactNode } from "react";
import { Meter, StatItem, UsagePending } from "@core/components/keys/parts";
import type { KeyCardProps, ProviderComponents } from "@core/contract";
import type { CredentialView } from "@core/api/types";
import type { T } from "@core/i18n";
import type { QuotaEntry } from "@core/lib/credentials";
import type { EditorState } from "@core/lib/editor";
import { fmtTokens, fmtUSD } from "@core/lib/format";
import { store, type State } from "@core/state/store";
import { capView, fmtPrice, summarizeKeys } from "./lib";
import type { KeyUsageView, ModelPricing, ModelView } from "./types";

// Totals over the keys whose balance has been loaded.
function KeySummary({ t, s, creds }: { t: T; s: State; creds: CredentialView[] }) {
  const sum = summarizeKeys(creds, s.quota);
  const blank = sum.loading ? "…" : "—";
  const balanceNote: string[] = [];
  if (sum.accounts > 1) balanceNote.push(t("summary.accounts", { n: sum.accounts }));
  if (sum.reserved > 0) balanceNote.push(t("usage.reserved", { v: fmtUSD(sum.reserved) }));
  const earnNote: string[] = [];
  if (sum.earners > 1) earnNote.push(t("summary.accounts", { n: sum.earners }));
  if (sum.referral > 0) earnNote.push(t("usage.referrer", { v: fmtUSD(sum.referral) }));
  const missingNote: string[] = [];
  if (sum.failed) missingNote.push(t("summary.failed", { n: sum.failed }));
  if (sum.pending) missingNote.push(t("summary.pending", { n: sum.pending }));
  return (
    <div className={"summary strip" + (sum.hasIssuer ? " has-earn" : "")} id="key-summary">
      <StatItem label={t("summary.balance")} value={sum.accounts ? fmtUSD(sum.available) : blank} note={balanceNote.join(" · ")} />
      {sum.hasIssuer ? <StatItem label={t("usage.earnings")} value={sum.earners ? fmtUSD(sum.earned) : blank} note={earnNote.join(" · ")} /> : null}
      <StatItem label={t("usage.spent24h")} value={sum.loaded ? fmtUSD(sum.daily) : blank} />
      <StatItem label={t("usage.spent7d")} value={sum.loaded ? fmtUSD(sum.weekly) : blank} />
      <StatItem label={t("usage.spent30d")} value={sum.loaded ? fmtUSD(sum.monthly) : blank} note={missingNote.join(" · ")} />
    </div>
  );
}

function CapBlock({ t, k }: { t: T; k: KeyUsageView }) {
  const cap = capView(k);
  const label = t("usage.cap") + (k.limit_reset ? " · " + k.limit_reset : "");
  return (
    <div className={"cap" + cap.level}>
      <div className="cap-head">
        <span>{label}</span>
        <strong>{t("usage.capLeft", { left: fmtUSD(cap.left), limit: fmtUSD(cap.limit) })}</strong>
      </div>
      <div className="track" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(cap.fraction * 100)}>
        <span className="fill" style={{ width: cap.width }}></span>
      </div>
    </div>
  );
}

// The top-right corner of a key with an issuing token: Provider earnings from
// the issuing API, kept apart from the API balance. Returns the amount for the
// name row and the note (or null) for the file row.
function earnCorner(t: T, q: QuotaEntry | undefined): [ReactNode, ReactNode] {
  const d = q && q.data;
  let value = q && q.state === "loading" ? "…" : "—";
  let valueClass = "";
  const notes: string[] = [];
  let noteClass = "";
  if (d && d.issuer) {
    const issuer = d.issuer;
    if (issuer.provider_usd !== undefined && issuer.provider_usd !== null) value = fmtUSD(issuer.provider_usd);
    else notes.push(t("usage.noProvider"));
    if (issuer.provider_frozen) {
      valueClass = " warn-text";
      notes.push(t("usage.frozen"));
    }
    if (Number(issuer.referrer_usd) > 0) notes.push(t("usage.referrer", { v: fmtUSD(issuer.referrer_usd) }));
  } else if (d && d.issuer_error) {
    notes.push(t("usage.earnFailed"));
    noteClass = " danger-text";
  }
  const note = notes.join(" · ");
  return [
    <div className="cred-earn">
      <span className="cred-earn-label">{t("usage.earnings")}</span>
      <span className={"cred-earn-value" + valueClass}>{value}</span>
    </div>,
    note ? <div className={"cred-earn-note" + noteClass} title={d && d.issuer_error ? d.issuer_error : note}>{note}</div> : null,
  ];
}

// The card's usage figures (or why there are none), then the spend cap and notes.
function KeyUsage({ t, cred, q }: KeyCardProps) {
  if (cred.read_error) return null;
  const d = q && q.data;
  if (!d) return <UsagePending t={t} cred={cred} q={q} />;
  const k = d.key;
  // Kept to the tooltip so the figures stay as tall as the facts beside them.
  const tip = d.balance
    ? [t("usage.balanceNote"), Number(d.balance.reserved_usd) > 0 ? t("usage.reserved", { v: fmtUSD(d.balance.reserved_usd) }) : ""].filter(Boolean).join(" · ")
    : "";
  return (
    <>
      <div className="meters">
        {d.balance
          ? <Meter label={t("usage.balance")} value={fmtUSD(d.balance.available_usd)} tip={tip} />
          : <Meter label={t("usage.balance")} value="—" note={d.balance_error || ""} noteClass="danger-text" />}
        <Meter label={t("usage.spent24h")} value={k ? fmtUSD(k.daily_usd) : "—"} />
        <Meter label={t("usage.spent7d")} value={k ? fmtUSD(k.weekly_usd) : "—"} />
        <Meter label={t("usage.spent30d")} value={k ? fmtUSD(k.monthly_usd) : "—"} />
      </div>
      {k && k.limit_usd !== undefined && k.limit_remaining_usd !== undefined ? <CapBlock t={t} k={k} /> : null}
      {!k && d.key_error ? <div className="usage-note danger-text">{t("usage.keyError", { msg: d.key_error })}</div> : null}
      {d.issuer_error ? <div className="usage-note danger-text">{t("usage.earnError", { msg: d.issuer_error })}</div> : null}
      {q && q.state === "error" ? <div className="usage-note danger-text">{t("usage.failed", { msg: q.error })}</div> : null}
    </>
  );
}

function PriceCell({ t, pricing }: { t: T; pricing: ModelPricing | undefined }) {
  const p = pricing || {};
  if (p.input === undefined && p.output === undefined) {
    return <td className="nowrap"><span className="sub">—</span></td>;
  }
  const cache: string[] = [];
  if (p.cache_read !== undefined) cache.push(t("catalog.priceCacheRead", { v: fmtPrice(p.cache_read) }));
  if (p.cache_write !== undefined) cache.push(t("catalog.priceCacheWrite", { v: fmtPrice(p.cache_write) }));
  return (
    <td className="nowrap">
      <div>{t("catalog.priceIO", { input: fmtPrice(p.input), output: fmtPrice(p.output) })}</div>
      {cache.length ? <div className="sub">{cache.join(" · ")}</div> : null}
    </td>
  );
}

function ModelCells({ t, m }: { t: T; m: ModelView }) {
  return (
    <>
      <td>
        <div className="mono strong">{m.id}</div>
        {m.name && m.name !== m.id ? <div className="sub">{m.name}</div> : null}
      </td>
      <td className="nowrap">{m.owned_by || "—"}</td>
      <td className="nowrap">
        <div>{m.context_length ? fmtTokens(m.context_length) : "—"}</div>
        {m.max_completion_tokens ? <div className="sub">{t("catalog.maxOut", { n: fmtTokens(m.max_completion_tokens) })}</div> : null}
      </td>
      <PriceCell t={t} pricing={m.pricing} />
    </>
  );
}

// The issuing token: an empty input keeps the saved one, the checkbox removes it.
function EditorFields({ t, ed }: { t: T; ed: EditorState }) {
  return (
    <div className="field">
      <label htmlFor="f-issuer">{t("form.issuer")}</label>
      <input
        className="input mono"
        id="f-issuer"
        type="password"
        spellCheck={false}
        autoComplete="new-password"
        placeholder={ed.issuerPlaceholder}
        disabled={ed.issuerClear}
        value={ed.issuer}
        onChange={(event) => store.updateEditor({ issuer: event.target.value })}
      />
      <label className={"check" + (ed.issuerSaved ? "" : " hidden")} id="row-issuer-clear">
        <input
          type="checkbox"
          id="f-issuer-clear"
          checked={ed.issuerClear}
          onChange={(event) => store.updateEditor(event.target.checked ? { issuerClear: true, issuer: "" } : { issuerClear: false })}
        />{" "}
        <span id="f-issuer-clear-label">{t("form.issuerClear", { token: ed.issuerSaved })}</span>
      </label>
      <span className="help">{t("form.issuerHelp")}</span>
    </div>
  );
}

export const components: ProviderComponents = {
  KeySummary,
  KeyUsage,
  keyCorner: (t, cred, q) => (!cred.read_error && cred.issuer_token_masked ? earnCorner(t, q) : [null, null]),
  modelColumns: ["col.model", "col.owner", "col.context", "col.pricePer1M"],
  ModelCells,
  EditorFields,
};
