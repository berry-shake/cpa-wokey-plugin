import type { ReactNode } from "react";
import type { ProxyView } from "../../api/types";
import { localeTag, type T } from "../../i18n";
import { credentialsOf } from "../../lib/credentials";
import { formatTime } from "../../lib/format";
import {
  DIRECT_KEY, egressName, egressPlace, INHERIT_KEY, proxyCheckKey, proxyUsage, unlistedProxyKeys, type ProxyCheckEntry,
} from "../../lib/proxies";
import { store, type State } from "../../state/store";

const COLUMNS = ["name", "url", "usage", "egress", "latency", "actions"];

function UsageCell({ t, n }: { t: T; n: number }) {
  return <td className="nowrap">{n ? t("proxies.usedBy", { n }) : <span className="sub">{t("proxies.unused")}</span>}</td>;
}

/** Exit IP and place, only once a check ran; "—" before. */
export function EgressCell({ t, entry }: { t: T; entry: ProxyCheckEntry | undefined }) {
  if (!entry) return <td className="proxy-egress"><span className="sub">—</span></td>;
  if (entry.state === "loading") return <td className="proxy-egress"><span className="sub">{t("proxies.checking")}</span></td>;
  const result = entry.result;
  if (entry.state === "error" || !result) {
    return (
      <td className="proxy-egress">
        <div className="danger-text">{t("proxies.failed")}</div>
        {entry.error ? <div className="sub proxy-error" title={entry.error}>{entry.error}</div> : null}
      </td>
    );
  }
  const via = egressName(result.egress);
  const place = egressPlace(result);
  return (
    <td className="proxy-egress" title={via ? t(via.key, via.params) : undefined}>
      <div className="mono strong">{result.ip || "—"}</div>
      {place ? <div className="sub">{place}</div> : null}
    </td>
  );
}

export function LatencyCell({ t, entry, locale }: { t: T; entry: ProxyCheckEntry | undefined; locale: string }) {
  const result = entry && entry.state === "ok" ? entry.result : undefined;
  if (!result || result.latency_ms === undefined) {
    return <td className="nowrap"><span className="sub">{entry && entry.state === "loading" ? "…" : "—"}</span></td>;
  }
  return (
    <td className="nowrap" title={result.checked_at ? t("proxies.checkedAt", { time: formatTime(result.checked_at, locale) }) : undefined}>
      {t("proxies.latency", { n: Math.round(result.latency_ms) })}
    </td>
  );
}

function CheckButton({ t, entry, onClick }: { t: T; entry: ProxyCheckEntry | undefined; onClick: () => void }) {
  const loading = !!entry && entry.state === "loading";
  return (
    <button type="button" className="btn btn-secondary btn-sm" disabled={loading} onClick={onClick}>
      {t(loading ? "proxies.checking" : "proxies.check")}
    </button>
  );
}

function BuiltinRow({ t, s, checkKey, name, address, used, locale }: {
  t: T;
  s: State;
  checkKey: string;
  name: string;
  address: ReactNode;
  used: number;
  locale: string;
}) {
  const entry = s.proxyChecks[checkKey];
  return (
    <tr className="proxy-builtin" data-key={checkKey}>
      <td>
        <div className="strong">{name}</div>
        <div className="sub">{t("proxies.builtin")}</div>
      </td>
      <td>{address}</td>
      <UsageCell t={t} n={used} />
      <EgressCell t={t} entry={entry} />
      <LatencyCell t={t} entry={entry} locale={locale} />
      <td>
        <div className="row-actions">
          <CheckButton t={t} entry={entry} onClick={() => void store.checkProxy(checkKey)} />
        </div>
      </td>
    </tr>
  );
}

function ProxyRow({ t, s, proxy, used, locale }: { t: T; s: State; proxy: ProxyView; used: number; locale: string }) {
  const key = proxyCheckKey(proxy.id);
  const entry = s.proxyChecks[key];
  const armed = s.armedDelete === "proxy:" + proxy.id;
  return (
    <tr data-key={key}>
      <td>
        <div className="strong">{proxy.name || proxy.label}</div>
      </td>
      <td><span className="mono">{proxy.url}</span></td>
      <UsageCell t={t} n={used} />
      <EgressCell t={t} entry={entry} />
      <LatencyCell t={t} entry={entry} locale={locale} />
      <td>
        <div className="row-actions">
          <CheckButton t={t} entry={entry} onClick={() => void store.checkProxy(key)} />
          <button type="button" className="btn btn-secondary btn-sm" onClick={() => store.openProxyDialog(proxy)}>
            {t("row.edit")}
          </button>
          <button
            type="button"
            className={"btn btn-danger btn-sm" + (armed ? " armed" : "")}
            disabled={!!s.busy["proxy-delete:" + proxy.id]}
            onClick={() => void store.deleteProxy(proxy)}
          >
            {t(armed ? "row.confirmDelete" : "row.delete")}
          </button>
        </div>
        {armed && used ? <div className="sub proxy-delete-note">{t("proxies.deleteUsedNote", { n: used })}</div> : null}
      </td>
    </tr>
  );
}

function ProxiesTable({ t, s }: { t: T; s: State }) {
  const data = s.proxies!;
  const usage = proxyUsage(credentialsOf(s.status));
  const locale = localeTag(s.lang);
  return (
    <div className="table-wrap">
      <table className="proxies-table">
        <thead>
          <tr>{COLUMNS.map((key) => <th key={key} className={"col-" + key}>{t("proxies.col." + key)}</th>)}</tr>
        </thead>
        <tbody>
          <BuiltinRow
            t={t}
            s={s}
            checkKey={INHERIT_KEY}
            name={t("proxies.inherit")}
            address={data.global_proxy ? <span className="mono">{data.global_proxy}</span> : <span className="sub">{t("proxies.inheritNone")}</span>}
            used={usage.inherit}
            locale={locale}
          />
          <BuiltinRow
            t={t}
            s={s}
            checkKey={DIRECT_KEY}
            name={t("proxies.direct")}
            address={<span className="sub">{t("proxies.directSub")}</span>}
            used={usage.direct}
            locale={locale}
          />
          {data.proxies.map((proxy) => (
            <ProxyRow key={proxy.id} t={t} s={s} proxy={proxy} used={usage.byId.get(proxy.id) || 0} locale={locale} />
          ))}
          {data.proxies.length === 0 ? (
            <tr>
              <td colSpan={COLUMNS.length} className="empty proxy-empty">{t("proxies.empty")}</td>
            </tr>
          ) : null}
        </tbody>
      </table>
    </div>
  );
}

export function ProxiesPanel({ t, s }: { t: T; s: State }) {
  const data = s.proxies;
  const unlisted = unlistedProxyKeys(credentialsOf(s.status));
  return (
    <section className="panel" role="tabpanel" id="panel-proxies" aria-labelledby="tab-proxies" hidden={s.tab !== "proxies"}>
      <section className="card">
        <div className="card-header">
          <div className="card-title">
            <h2>{t("proxies.title")}</h2>
            <span className="hint">{t("proxies.hint")}</span>
          </div>
          <div className="actions">
            {unlisted.length && data ? (
              <button
                id="proxies-import"
                className="btn btn-secondary btn-sm"
                type="button"
                title={t("proxies.importHint", { n: unlisted.length })}
                disabled={!!s.busy["proxy-import"]}
                onClick={() => void store.importProxies()}
              >
                {t("proxies.import")}
              </button>
            ) : null}
            <button
              id="proxies-check-all"
              className="btn btn-secondary btn-sm"
              type="button"
              disabled={!data || !!s.busy["proxy-check-all"]}
              onClick={() => void store.checkAllProxies()}
            >
              {t("proxies.checkAll")}
            </button>
            <button id="proxies-add" className="btn btn-primary btn-sm" type="button" onClick={() => store.openProxyDialog()}>
              {t("proxies.add")}
            </button>
          </div>
        </div>
        <div id="proxies-body">
          {data && data.load_error ? (
            <div className="notice catalog-notice" data-state="error">
              <span className="notice-text">{t("proxies.loadError", { msg: data.load_error })}</span>
            </div>
          ) : null}
          {data ? (
            <ProxiesTable t={t} s={s} />
          ) : (
            <div className={"empty" + (s.proxiesError ? " danger-text" : "")}>
              {s.proxiesError ? t("proxies.loadError", { msg: s.proxiesError }) : t("proxies.loading")}
            </div>
          )}
        </div>
      </section>
    </section>
  );
}
