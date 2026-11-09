import type { CredentialView, LogEntry } from "../../api/types";
import type { T } from "../../i18n";
import { fmtCount, fmtMS, fmtPct, fmtRate, formatWhen } from "../../lib/format";
import { cacheRate, latTone, logAuthIndex, logCredential, logKey, logRequestID } from "../../lib/logs";

export const LOG_COLUMNS = ["time", "model", "effort", "result", "cred", "timing", "usage"];

function LatLine({ t, kind, ms, outTokens }: { t: T; kind: "ttft" | "total"; ms: number | undefined; outTokens: number }) {
  return (
    <div className={"lat-line " + latTone(ms, kind, outTokens)} title={t(kind === "ttft" ? "logs.ttftHint" : "logs.totalHint")}>
      <span>{t(kind === "ttft" ? "logs.ttft" : "logs.total")}</span>
      <strong>{fmtMS(ms)}</strong>
    </div>
  );
}

function DetailItem({ label, value, className }: { label: string; value: string; className?: string }) {
  return (
    <div className={className || ""}>
      <span className="sub">{label}</span>
      <span className="mono">{value}</span>
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

/**
 * One entry: the summary line, and the details when expanded. The auth
 * index and key come from the current credentials, which may load after
 * the log.
 */
export function LogRow({ t, e, creds, locale }: { t: T; e: LogEntry; creds: CredentialView[]; locale: string }) {
  const ok = e.status === "success";
  const rate = cacheRate(e);
  const label = (c: string) => t("logs.col." + c);
  return (
    <details className="log-row">
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
        <div data-label={label("result")}>
          <span className={"badge " + (ok ? "ok" : "danger")} title={e.error || ""}>{e.http_status ? "HTTP " + e.http_status : t("logs.error")}</span>
          <div className="sub">{e.stream ? t("logs.stream") : t("logs.nonStream")}</div>
        </div>
        <div className="clip" title={logKey(e, creds)} data-label={label("cred")}>{logCredential(e, creds)}</div>
        <div data-label={label("timing")}>
          <div className="log-lat">
            <LatLine t={t} kind="ttft" ms={e.ttft_ms} outTokens={e.output_tokens} />
            <LatLine t={t} kind="total" ms={e.duration_ms} outTokens={e.output_tokens} />
          </div>
        </div>
        <div className="log-usage" data-label={label("usage")}>
          <div title={t("logs.usageHint")}>{fmtCount(e.input_tokens) + " / " + fmtCount(e.output_tokens)}</div>
          <div className="sub" title={t("logs.speedHint")}>
            {t("logs.cacheLine", { c: fmtCount(e.cached_tokens), p: rate === null ? "—" : fmtPct(rate) }) + " · " + fmtRate(e.output_tokens_per_second)}
          </div>
        </div>
        <span className="log-chev" aria-hidden="true">›</span>
      </summary>
      <div className="log-detail">
        <DetailItem label={t("logs.d.reqid")} value={logRequestID(e)} className="reqid" />
        <DetailItem label={t("logs.d.endpoint")} value={e.endpoint || "—"} />
        <DetailItem label={t("logs.d.stream")} value={e.stream ? t("logs.yes") : t("logs.no")} />
        <DetailItem label={t("logs.d.status")} value={String(e.http_status || "—")} />
        <DetailItem label={t("logs.d.auth")} value={logAuthIndex(e, creds)} />
        <DetailItem label={t("logs.d.key")} value={logKey(e, creds) || "—"} />
        <DetailItem label={t("logs.d.cacheWrite")} value={String(e.cache_write_tokens || 0)} />
        <DetailItem label={t("logs.d.reasoning")} value={String(e.reasoning_tokens || 0)} />
        <DetailItem label={t("logs.d.speed")} value={fmtRate(e.output_tokens_per_second)} />
        {e.error_code ? <DetailItem label={t("logs.d.code")} value={e.error_code} /> : null}
        {e.error_phase ? <DetailItem label={t("logs.d.phase")} value={t("logs.phase." + e.error_phase)} /> : null}
        {e.error ? <DetailItem label={t("logs.d.error")} value={e.error} className="err danger-text" /> : null}
      </div>
    </details>
  );
}
