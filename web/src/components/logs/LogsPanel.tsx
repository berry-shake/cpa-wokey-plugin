import type { LogPage } from "../../api/types";
import { localeTag, type T } from "../../i18n";
import { allCredentials } from "../../lib/credentials";
import { fmtCount, fmtPct, fmtRate, fmtTokens } from "../../lib/format";
import { store, type State } from "../../state/store";
import { LogHead, LogRow } from "./LogRow";
import { Pager } from "./Pager";
import { RangeTools } from "./RangeTools";

function StripItem({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="strip-item">
      <div className="strip-label">{label}</div>
      <div className="strip-line"><span className="strip-value">{value}</span></div>
      {note ? <div className="strip-note">{note}</div> : null}
    </div>
  );
}

function LogSummaryStrip({ t, page }: { t: T; page: LogPage }) {
  const sum = page.summary || ({} as Partial<LogPage["summary"]>);
  return (
    <>
      <StripItem label={t("logs.sum.requests")} value={String(sum.requests || 0)} note={t("logs.sum.split", { ok: sum.success || 0, bad: sum.failed || 0 })} />
      <StripItem
        label={t("logs.sum.tokens")}
        value={fmtTokens((sum.input_tokens || 0) + (sum.output_tokens || 0))}
        note={t("logs.sum.io", { i: fmtTokens(sum.input_tokens), o: fmtTokens(sum.output_tokens), c: fmtTokens(sum.cached_tokens) })}
      />
      <StripItem label={t("logs.sum.cache")} value={fmtPct(sum.cache_hit_rate)} />
      <StripItem label={t("logs.sum.speed")} value={fmtRate(sum.output_tokens_per_second)} />
    </>
  );
}

function retentionHint(t: T, page: LogPage): string {
  if (!(page.retention > 0)) return t("logs.hintOff");
  return page.retention_days > 0
    ? t("logs.hintDays", { n: fmtCount(page.retention), d: page.retention_days })
    : t("logs.hint", { n: fmtCount(page.retention) });
}

export function LogsPanel({ t, s }: { t: T; s: State }) {
  const logs = s.logs;
  const page = logs.page;
  const items = page ? page.items || [] : [];
  const creds = allCredentials(s.status);
  const locale = localeTag(s.lang);

  let body;
  if (!page) body = <div className="empty">{t("keys.notLoaded")}</div>;
  else if (!items.length) body = <div className="empty">{t(logs.filtered ? "logs.noMatch" : "logs.empty")}</div>;
  else body = (
    <>
      <LogHead t={t} />
      {items.map((e) => <LogRow key={e.id} t={t} e={e} creds={creds} locale={locale} />)}
    </>
  );

  return (
    <section className="panel" role="tabpanel" id="panel-logs" aria-labelledby="tab-logs" hidden={s.tab !== "logs"}>
      <RangeTools t={t} logs={logs} focusStart={s.focusRangeStart} />
      <div className="notice" id="logs-error" data-state="error" role="alert" hidden={!(page && page.error)}>
        <span className="notice-text" id="logs-error-text">{page && page.error ? t("logs.storeError", { msg: page.error }) : ""}</span>
      </div>
      <div className="summary strip" id="logs-summary">{page ? <LogSummaryStrip t={t} page={page} /> : null}</div>
      <section className="card">
        <div className="card-header">
          <div className="card-title">
            <h2>{t("logs.title")}</h2>
            <span className="hint" id="logs-hint">{page ? retentionHint(t, page) : ""}</span>
          </div>
          <div className="actions">
            <button id="logs-refresh" className="btn btn-secondary btn-sm" type="button" disabled={!!s.busy.logs} onClick={() => store.loadLogs(false).catch(() => {})}>
              {t("logs.refresh")}
            </button>
            <button id="logs-clear" className={"btn btn-danger btn-sm" + (logs.clearArmed ? " armed" : "")} type="button" onClick={() => void store.clearLogs()}>
              {t(logs.clearArmed ? "logs.confirmClear" : "logs.clear")}
            </button>
          </div>
        </div>
        <div className="toolbar">
          <input
            className="input input-sm search"
            id="logs-search"
            type="search"
            spellCheck={false}
            placeholder={t("logs.search")}
            value={logs.search}
            onChange={(event) => store.setLogsSearch(event.target.value)}
          />
          <select className="input input-sm" id="logs-status" value={logs.status} onChange={(event) => store.setLogsStatus(event.target.value)}>
            <option value="all">{t("logs.all")}</option>
            <option value="success">{t("logs.success")}</option>
            <option value="error">{t("logs.error")}</option>
          </select>
          <span className="count" id="logs-count">{page ? t("logs.count", { n: page.total }) : ""}</span>
        </div>
        <div className="log-grid" id="logs-body">{body}</div>
        <div className="pager" id="logs-pager">
          {page && items.length ? <Pager t={t} page={page} shown={items.length} size={logs.size} /> : null}
        </div>
      </section>
    </section>
  );
}
