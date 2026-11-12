import type { ReactNode } from "react";
import type { CredentialView, KeyUsageView } from "../../api/types";
import type { T } from "../../i18n";
import { capView, credState, credSubline, displayName, isDirect, type QuotaEntry } from "../../lib/credentials";
import { fmtUSD, formatTime } from "../../lib/format";
import { store } from "../../state/store";

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

// One figure of a card's usage row: label, value, optional note below.
function Meter({ label, value, note, noteClass, tip }: { label: string; value: string; note?: string; noteClass?: string; tip?: string }) {
  return (
    <div className="meter">
      <div className="meter-label" title={tip ? label + " · " + tip : label}>{label}</div>
      <div className="meter-value" title={tip ? value + " · " + tip : value}>{value}</div>
      {note ? <div className={"meter-note" + (noteClass ? " " + noteClass : "")} title={note}>{note}</div> : null}
    </div>
  );
}

// Four facts laid out like the usage figures; details are in the tooltip.
function Fact({ label, text, tip, extra, flag }: { label: string; text: string; tip?: string; extra?: ReactNode; flag?: string }) {
  return (
    <div className="meter">
      <div className="meter-label" title={label}>{label}{flag ? <span className="warn-text">{" · " + flag}</span> : null}</div>
      <div className="meter-value text" title={tip || text}>{text}{extra || null}</div>
    </div>
  );
}

function Facts({ t, cred }: { t: T; cred: CredentialView }) {
  const models = Array.isArray(cred.models) ? cred.models : [];
  const missing = Array.isArray(cred.missing_models) ? cred.missing_models : [];
  const modelsText = cred.all_models ? t("models.all") : t("models.selected", { count: models.length });
  const prefixText = cred.effective_prefix ? cred.effective_prefix + "/" : t("prefix.none");
  let proxyText = t("proxy.inherit");
  let proxyTip: string | undefined;
  if (cred.proxy_name) {
    proxyText = cred.proxy_name;
    proxyTip = cred.proxy_url;
  } else if (cred.proxy_url) {
    proxyText = isDirect(cred.proxy_url) ? t("proxy.direct") : cred.proxy_url;
  }
  const ok = cred.success || 0;
  const failed = cred.failed || 0;
  return (
    <div className="facts">
      <Fact
        label={t("card.models")}
        text={modelsText}
        tip={[
          modelsText,
          cred.registered_known ? t("models.registered", { count: cred.registered_models }) : "",
          missing.length ? t("models.missing", { list: missing.join(", ") }) : "",
        ].filter(Boolean).join("\n")}
        flag={missing.length ? t("models.missingCount", { count: missing.length }) : ""}
      />
      <Fact
        label={t("card.prefix")}
        text={prefixText}
        tip={prefixText + "\n" + t(cred.prefix === null || cred.prefix === undefined ? "prefix.default" : "prefix.custom")}
      />
      <Fact label={t("card.proxy")} text={proxyText} tip={proxyTip} />
      <Fact
        label={t("card.requestsLabel")}
        text={ok + " / "}
        tip={t("card.requests", { ok, failed })}
        extra={<span className={failed ? "danger-text" : ""}>{String(failed)}</span>}
      />
    </div>
  );
}

// The card's usage figures (or why there are none), then the spend cap and notes.
function UsageParts({ t, cred, q }: { t: T; cred: CredentialView; q: QuotaEntry | undefined }) {
  if (cred.read_error) return null;
  const d = q && q.data;
  if (!d) {
    const load = () => void store.loadQuota(cred.auth_index);
    return (
      <div className="usage-state">
        {q && q.state === "loading" ? (
          <span>{t("usage.loading")}</span>
        ) : q && q.state === "error" ? (
          <>
            <span className="danger-text">{t("usage.failed", { msg: q.error })}</span>
            <button type="button" className="btn btn-secondary btn-sm" onClick={load}>{t("usage.retry")}</button>
          </>
        ) : (
          <>
            <span>{t(cred.disabled ? "usage.disabled" : "usage.notLoaded")}</span>
            <button type="button" className="btn btn-secondary btn-sm" onClick={load}>{t("usage.check")}</button>
          </>
        )}
      </div>
    );
  }
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

export function CredCard({ t, cred, q, armed, busy, locale }: {
  t: T;
  cred: CredentialView;
  q: QuotaEntry | undefined;
  armed: boolean;
  busy: Record<string, true>;
  locale: string;
}) {
  const [badgeClass, badgeKey] = credState(cred);
  // Why the key is off or cooling down, under the figures on its own line.
  const stateNote = cred.read_error || cred.status_message || "";
  const name = displayName(cred);
  const [earn, earnNote] = !cred.read_error && cred.issuer_token_masked ? earnCorner(t, q) : [null, null];
  return (
    <article className={"cred" + (cred.disabled ? " off" : "")}>
      <div className="cred-head">
        <div className="cred-row">
          <div className="cred-title">
            <span className="cred-name" title={name}>{name}</span>
            <span className={"badge " + badgeClass}>{t(badgeKey)}</span>
          </div>
          {earn}
        </div>
        <div className="cred-row">
          <div className="cred-file" title={cred.name}>{credSubline(cred, t("card.priority"))}</div>
          {earnNote}
        </div>
      </div>
      <div className="cred-body">
        {cred.read_error ? null : <Facts t={t} cred={cred} />}
        <UsageParts t={t} cred={cred} q={q} />
        {cred.note ? <div className="usage-note sub" title={cred.note}>{t("card.noteLine", { msg: cred.note })}</div> : null}
        {stateNote ? <div className={"usage-note " + (cred.read_error ? "danger-text" : "warn-text")}>{t("card.statusNote", { msg: stateNote })}</div> : null}
      </div>
      <div className="cred-foot">
        <div className="cred-meta">
          {q && q.data && q.data.fetched_at ? <span>{t("usage.fetched", { time: formatTime(q.data.fetched_at, locale) })}</span> : null}
          {!cred.read_error && q && q.data ? (
            <button type="button" className="btn-link" disabled={q.state === "loading"} onClick={() => void store.loadQuota(cred.auth_index)}>
              {t(q.state === "loading" ? "usage.loading" : "usage.refresh")}
            </button>
          ) : null}
        </div>
        <div className="row-actions">
          <button type="button" className="btn btn-secondary btn-sm" disabled={!!cred.read_error} onClick={() => store.openEditor("edit", cred)}>
            {t("row.edit")}
          </button>
          <button type="button" className="btn btn-secondary btn-sm" disabled={!!busy["toggle:" + cred.name]} onClick={() => void store.toggleDisabled(cred)}>
            {t(cred.disabled ? "row.enable" : "row.disable")}
          </button>
          <button
            type="button"
            className={"btn btn-danger btn-sm" + (armed ? " armed" : "")}
            disabled={!!busy["delete:" + cred.name]}
            onClick={() => void store.deleteCredential(cred)}
          >
            {t(armed ? "row.confirmDelete" : "row.delete")}
          </button>
        </div>
      </div>
    </article>
  );
}
