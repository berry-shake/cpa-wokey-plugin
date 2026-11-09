import { useEffect, useState, type ReactNode } from "react";
import type { CredentialView, LogEntry } from "../../api/types";
import type { T } from "../../i18n";
import { errorMessage } from "../../api/client";
import { fmtCount, fmtMS, fmtPct, fmtRate, formatWhen } from "../../lib/format";
import {
  attemptCount,
  cacheRate,
  finishKey,
  latTone,
  logAuthIndex,
  logCredential,
  logEnd,
  logKey,
  logLabel,
  logRequestID,
  usageMissing,
} from "../../lib/logs";
import { store } from "../../state/store";

export const LOG_COLUMNS = ["time", "model", "effort", "result", "cred", "timing", "usage"];

function LatLine({ t, kind, ms, outTokens }: { t: T; kind: "ttft" | "total"; ms: number | undefined; outTokens: number }) {
  return (
    <div className={"lat-line " + latTone(ms, kind, outTokens)} title={t(kind === "ttft" ? "logs.ttftHint" : "logs.totalHint")}>
      <span>{t(kind === "ttft" ? "logs.ttft" : "logs.total")}</span>
      <strong>{fmtMS(ms)}</strong>
    </div>
  );
}

function Datum({ label, mono, className, children }: { label: string; mono?: boolean; className?: string; children: ReactNode }) {
  return (
    <div className={"datum" + (className ? " " + className : "")}>
      <div className="datum-label">{label}</div>
      <div className={"datum-value" + (mono ? " mono" : "")}>{children}</div>
    </div>
  );
}

export function LogHead({ t }: { t: T }) {
  return (
    <div className="log-head">
      {LOG_COLUMNS.map((c) => <div key={c}>{t("logs.col." + c)}</div>)}
      <div></div>
    </div>
  );
}

/** The result tag: the status CPA got, or "failed" without one. */
function ResultTag({ t, e }: { t: T; e: LogEntry }) {
  const ok = e.status === "success";
  return (
    <span className={"badge tag " + (ok ? "ok" : "danger")} title={e.error || ""}>
      {e.http_status ? "HTTP " + e.http_status : t("logs.error")}
    </span>
  );
}

function egressText(t: T, egress: string | undefined): string {
  if (!egress) return "—";
  if (egress === "direct") return t("proxy.direct");
  if (egress === "env") return t("logs.egress.env");
  return egress;
}

function Finish({ t, e }: { t: T; e: LogEntry }) {
  const reason = e.finish_reason || "";
  if (!reason) return <>{e.usage_reported === undefined ? "—" : t("logs.notReported")}</>;
  const key = finishKey(reason);
  if (!key) return <span className="mono">{reason}</span>;
  return (
    <>
      {t(key)}
      <span className="datum-note mono">{reason}</span>
    </>
  );
}

function UpstreamStatus({ t, e }: { t: T; e: LogEntry }) {
  if (e.upstream_status) {
    const ok = e.upstream_status >= 200 && e.upstream_status < 300;
    return <span className={ok ? "" : "danger-text"}>{"HTTP " + e.upstream_status}</span>;
  }
  // Entries since 0.1.9 always name the egress; without a status no response arrived.
  return <>{e.egress ? t("logs.noResponse") : "—"}</>;
}

function Frames({ t, e }: { t: T; e: LogEntry }) {
  if (e.frames === undefined) return <>{"—"}</>;
  return (
    <>
      {fmtCount(e.frames)}
      {e.last_frame_ms !== undefined ? <span className="datum-note">{t("logs.lastFrame", { t: fmtMS(e.last_frame_ms) })}</span> : null}
    </>
  );
}

interface AttemptState {
  /** request ID and count the list was loaded for */
  key: string;
  items: LogEntry[] | null;
  error: string;
}

/** The credentials CPA tried for one request, loaded when the row opens. */
function Attempts({ t, e, n, creds, state }: { t: T; e: LogEntry; n: number; creds: CredentialView[]; state: AttemptState | null }) {
  let body: ReactNode;
  if (!state || (!state.items && !state.error)) body = <div className="sub">{t("logs.attemptsLoading")}</div>;
  else if (state.error) body = <div className="danger-text">{state.error}</div>;
  else
    body = (state.items || []).map((a, i) => (
      <div className={"attempt" + (a.id === e.id ? " current" : "")} key={a.id}>
        <span className="mono attempt-n">{i + 1 + "."}</span>
        <span><ResultTag t={t} e={a} /></span>
        <span className="attempt-cred" title={logKey(a, creds)}>
          {logCredential(a, creds)}
          {a.id === e.id ? <span className="attempt-this">{t("logs.attemptThis")}</span> : null}
        </span>
        <span className="attempt-dur">{fmtMS(a.duration_ms)}</span>
        <span className={a.error ? "danger-text attempt-err" : "sub attempt-err"}>{a.error || "—"}</span>
      </div>
    ));
  return (
    <div className="log-attempts">
      <div className="log-attempts-head">{t("logs.attempts", { n })}</div>
      {body}
    </div>
  );
}

/**
 * One entry: the summary line, and the details when expanded. The auth
 * index and key come from the current credentials, which may load after
 * the log.
 */
export function LogRow({ t, e, creds, locale }: { t: T; e: LogEntry; creds: CredentialView[]; locale: string }) {
  const ok = e.status === "success";
  const rate = cacheRate(e);
  const credLabel = logLabel(e, creds);
  const label = (c: string) => t("logs.col." + c);
  const noUsage = usageMissing(e);
  const tokens = (n: number | undefined) => (noUsage ? t("logs.notReported") : fmtCount(n || 0));
  const end = logEnd(e);
  const tries = attemptCount(e);
  const [open, setOpen] = useState(false);
  const [attempts, setAttempts] = useState<AttemptState | null>(null);
  const wanted = tries > 1 ? (e.request_id || "") + ":" + tries : "";

  useEffect(() => {
    if (!open || !wanted || (attempts && attempts.key === wanted)) return;
    const next: AttemptState = { key: wanted, items: null, error: "" };
    setAttempts(next);
    store.logAttempts(e.request_id || "").then(
      (items) => setAttempts((cur) => (cur === next ? { ...next, items } : cur)),
      (error) => setAttempts((cur) => (cur === next ? { ...next, error: errorMessage(error) } : cur)),
    );
    // Only a newly opened row or a changed attempt count triggers a load.
  }, [open, wanted]);

  return (
    <details className="log-row" onToggle={(ev) => setOpen((ev.currentTarget as HTMLDetailsElement).open)}>
      <summary>
        <div className="nowrap" data-label={label("time")}>{formatWhen(e.time, locale)}</div>
        <div className="log-model" data-label={label("model")}>
          <div className="mono strong" title={e.model || ""}>{e.model || t("logs.notRecorded")}</div>
          <div className="mono sub" title={e.upstream_model || ""}>{e.upstream_model || t("logs.notRecorded")}</div>
          <div className="mono served" title={t("logs.servedHint", { m: e.served_model || t("logs.notReported") })}>
            {"↳ " + (e.served_model || t("logs.notReported"))}
          </div>
        </div>
        <div data-label={label("effort")}>{e.reasoning_effort || "—"}</div>
        <div className="log-result" data-label={label("result")}>
          <ResultTag t={t} e={e} />
          <div className="sub" title={tries > 1 ? t("logs.attemptsHint", { n: tries }) : undefined}>
            {(e.stream ? t("logs.stream") : t("logs.nonStream")) + (tries > 1 ? " · ×" + tries : "")}
          </div>
        </div>
        <div className="clip" title={logKey(e, creds)} data-label={label("cred")}>{logCredential(e, creds)}</div>
        <div data-label={label("timing")}>
          <div className="log-lat">
            <LatLine t={t} kind="ttft" ms={e.ttft_ms} outTokens={e.output_tokens} />
            <LatLine t={t} kind="total" ms={e.duration_ms} outTokens={e.output_tokens} />
          </div>
        </div>
        <div className="log-usage" data-label={label("usage")}>
          {noUsage ? (
            <>
              <div title={t("logs.noUsageHint")}>{t("logs.notReported")}</div>
              <div className="sub">—</div>
            </>
          ) : (
            <>
              <div title={t("logs.usageHint")}>{fmtCount(e.input_tokens) + " / " + fmtCount(e.output_tokens)}</div>
              <div className="sub" title={t("logs.speedHint")}>
                {t("logs.cacheLine", { c: fmtCount(e.cached_tokens), p: rate === null ? "—" : fmtPct(rate) }) + " · " + fmtRate(e.output_tokens_per_second)}
              </div>
            </>
          )}
        </div>
        <span className="log-chev" aria-hidden="true">›</span>
      </summary>
      <div className="log-detail">
        <div className="log-detail-head">{t("logs.d.title")}</div>
        <div className="log-detail-grid">
          <Datum label={t("logs.d.reqid")} mono className="reqid wide">{logRequestID(e)}</Datum>
          <Datum label={t("logs.d.start")}>{formatWhen(e.time, locale)}</Datum>
          <Datum label={t("logs.d.end")}>{end ? formatWhen(end.toISOString(), locale) : "—"}</Datum>

          <Datum label={t("logs.d.client")} mono={!!e.user_agent} className="wide">{e.user_agent || "—"}</Datum>
          <Datum label={t("logs.d.clientPath")} mono={!!e.client_path}>{e.client_path || "—"}</Datum>
          <Datum label={t("logs.d.endpoint")} mono>{e.endpoint || "—"}</Datum>

          <Datum label={t("logs.d.model")} mono={!!e.model}>{e.model || t("logs.notRecorded")}</Datum>
          <Datum label={t("logs.d.upstream")} mono={!!e.upstream_model}>{e.upstream_model || t("logs.notRecorded")}</Datum>
          <Datum label={t("logs.d.served")} mono={!!e.served_model}>{e.served_model || t("logs.notReported")}</Datum>

          <Datum label={t("logs.d.cred")} className="datum-cred">
            {logCredential(e, creds)}
            {credLabel ? <span className="datum-note">{credLabel}</span> : null}
          </Datum>
          <Datum label={t("logs.d.key")} mono>{logKey(e, creds) || "—"}</Datum>
          <Datum label={t("logs.d.auth")} mono>{logAuthIndex(e, creds)}</Datum>

          <Datum label={t("logs.d.result")} className="datum-result">
            <span className={ok ? "" : "danger-text"}>{e.http_status ? "HTTP " + e.http_status : t("logs.error")}</span>
            {" · " + (e.stream ? t("logs.stream") : t("logs.nonStream"))}
          </Datum>
          <Datum label={t("logs.d.upstreamStatus")} className="datum-upstatus"><UpstreamStatus t={t} e={e} /></Datum>
          <Datum label={t("logs.d.finish")} className="datum-finish"><Finish t={t} e={e} /></Datum>

          <Datum label={t("logs.d.ttft")}>{fmtMS(e.ttft_ms)}</Datum>
          <Datum label={t("logs.d.total")}>{fmtMS(e.duration_ms)}</Datum>
          <Datum label={t("logs.d.speed")}>{noUsage ? "—" : fmtRate(e.output_tokens_per_second)}</Datum>

          <Datum label={t("logs.d.input")}>{tokens(e.input_tokens)}</Datum>
          <Datum label={t("logs.d.output")}>{tokens(e.output_tokens)}</Datum>
          <Datum label={t("logs.d.reasoning")}>{tokens(e.reasoning_tokens)}</Datum>

          <Datum label={t("logs.d.cacheRead")}>{tokens(e.cached_tokens) + (noUsage || rate === null ? "" : " (" + fmtPct(rate) + ")")}</Datum>
          <Datum label={t("logs.d.cacheWrite")}>{tokens(e.cache_write_tokens)}</Datum>
          <Datum label={t("logs.d.effort")}>{e.reasoning_effort || "—"}</Datum>

          <Datum label={t("logs.d.egress")} mono={!!e.egress && e.egress !== "direct" && e.egress !== "env"}>{egressText(t, e.egress)}</Datum>
          <Datum label={t("logs.d.upstreamReqid")} mono={!!e.upstream_request_id} className="wide">{e.upstream_request_id || "—"}</Datum>
          <Datum label={t("logs.d.frames")} className="datum-frames"><Frames t={t} e={e} /></Datum>

          {e.error_code ? <Datum label={t("logs.d.code")} mono>{e.error_code}</Datum> : null}
          {e.error_phase ? <Datum label={t("logs.d.phase")}>{t("logs.phase." + e.error_phase)}</Datum> : null}
          {e.retry_after ? <Datum label={t("logs.d.retryAfter")}>{t("logs.seconds", { n: e.retry_after })}</Datum> : null}
        </div>
        {e.error ? (
          <div className="log-detail-error">
            <div className="datum-label">{t("logs.d.error")}</div>
            <div className="danger-text">{e.error}</div>
          </div>
        ) : null}
        {tries > 1 ? <Attempts t={t} e={e} n={tries} creds={creds} state={attempts} /> : null}
      </div>
    </details>
  );
}
