import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent, type ReactNode } from "react";
import type { ProxyView } from "../../api/types";
import { localeTag, type T } from "../../i18n";
import { credentialsOf } from "../../lib/credentials";
import { formatWhen } from "../../lib/format";
import {
  checkedAtText, DIRECT_KEY, dropIndex, egressName, egressPlace, flagUrl, INHERIT_KEY, moveItem, proxyCheckKey, proxyUsage, shownCheck,
  unlistedProxyKeys, type ProxyCheckEntry,
} from "../../lib/proxies";
import { store, type State } from "../../state/store";

const COLUMNS = ["name", "url", "usage", "egress", "latency", "checked", "actions"];

function UsageCell({ t, n }: { t: T; n: number }) {
  return <td className="nowrap">{n ? t("proxies.usedBy", { n }) : <span className="sub">{t("proxies.unused")}</span>}</td>;
}

/** Flag and exit IP, then country · city; "—" until the first check. */
export function EgressCell({ t, entry, locale }: { t: T; entry: ProxyCheckEntry | undefined; locale: string }) {
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
  const place = egressPlace(result, locale);
  const flag = flagUrl(result.country);
  return (
    <td className="proxy-egress" title={via ? t(via.key, via.params) : undefined}>
      <div className="proxy-ip">
        {flag ? <img className="proxy-flag" src={flag} alt={result.country} width={18} height={18} loading="lazy" /> : null}
        <span className="mono strong">{result.ip || "—"}</span>
      </div>
      {place ? <div className="sub">{place}</div> : null}
    </td>
  );
}

/** When the shown result was checked: the time today, else the date and time. */
export function CheckedCell({ entry, locale }: { entry: ProxyCheckEntry | undefined; locale: string }) {
  const at = entry && entry.state !== "loading" && entry.result ? entry.result.checked_at : undefined;
  if (!at) return <td className="nowrap"><span className="sub">{entry && entry.state === "loading" ? "…" : "—"}</span></td>;
  return <td className="nowrap proxy-checked" title={formatWhen(at, locale)}>{checkedAtText(at)}</td>;
}

export function LatencyCell({ t, entry }: { t: T; entry: ProxyCheckEntry | undefined }) {
  const result = entry && entry.state === "ok" ? entry.result : undefined;
  if (!result || result.latency_ms === undefined) {
    return <td className="nowrap"><span className="sub">{entry && entry.state === "loading" ? "…" : "—"}</span></td>;
  }
  return <td className="nowrap">{t("proxies.latency", { n: Math.round(result.latency_ms) })}</td>;
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
  const entry = shownCheck(checkKey, s.proxyChecks, s.proxies);
  return (
    <tr className="proxy-builtin" data-key={checkKey}>
      <td>
        <div className="proxy-name">
          {/* Built-in rows stay on top; the spacer lines their names up with the handles below. */}
          <span className="drag-spacer" aria-hidden="true" />
          <div>
            <div className="strong">{name}</div>
            <div className="sub">{t("proxies.builtin")}</div>
          </div>
        </div>
      </td>
      <td>{address}</td>
      <UsageCell t={t} n={used} />
      <EgressCell t={t} entry={entry} locale={locale} />
      <LatencyCell t={t} entry={entry} />
      <CheckedCell entry={entry} locale={locale} />
      <td>
        <div className="row-actions">
          <CheckButton t={t} entry={entry} onClick={() => void store.checkProxy(checkKey)} />
        </div>
      </td>
    </tr>
  );
}

const grip = (
  <svg viewBox="0 0 10 16" width="10" height="16" aria-hidden="true" fill="currentColor">
    <circle cx="2.5" cy="3" r="1.4" /><circle cx="7.5" cy="3" r="1.4" />
    <circle cx="2.5" cy="8" r="1.4" /><circle cx="7.5" cy="8" r="1.4" />
    <circle cx="2.5" cy="13" r="1.4" /><circle cx="7.5" cy="13" r="1.4" />
  </svg>
);

/** Where the dragged row would land, drawn as a line above or below a row. */
type DropMark = "drop-before" | "drop-after" | "";

function ProxyRow({ t, s, proxy, used, locale, dragging, mark, onGrab }: {
  t: T;
  s: State;
  proxy: ProxyView;
  used: number;
  locale: string;
  dragging: boolean;
  mark: DropMark;
  onGrab: (event: ReactPointerEvent<HTMLButtonElement>, id: string) => void;
}) {
  const key = proxyCheckKey(proxy.id);
  const entry = shownCheck(key, s.proxyChecks, s.proxies);
  const armed = s.armedDelete === "proxy:" + proxy.id;
  const name = proxy.name || proxy.label;
  const onKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    const delta = event.key === "ArrowUp" ? -1 : event.key === "ArrowDown" ? 1 : 0;
    if (!delta) return;
    event.preventDefault();
    const handle = event.currentTarget;
    store.moveProxy(proxy.id, delta);
    // The row moves in the DOM, which can take focus away from its handle.
    requestAnimationFrame(() => handle.focus());
  };
  return (
    <tr data-key={key} data-proxy-id={proxy.id} className={[dragging ? "is-dragging" : "", mark].filter(Boolean).join(" ") || undefined}>
      <td>
        <div className="proxy-name">
          <button
            type="button"
            className="drag-handle"
            data-handle={proxy.id}
            title={t("proxies.dragHint")}
            aria-label={t("proxies.dragLabel", { name })}
            onPointerDown={(event) => onGrab(event, proxy.id)}
            onKeyDown={onKeyDown}
          >
            {grip}
          </button>
          <div className="strong">{name}</div>
        </div>
      </td>
      <td><span className="mono">{proxy.url}</span></td>
      <UsageCell t={t} n={used} />
      <EgressCell t={t} entry={entry} locale={locale} />
      <LatencyCell t={t} entry={entry} />
      <CheckedCell entry={entry} locale={locale} />
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

interface Drag {
  id: string;
  from: number;
  to: number;
}

function ProxiesTable({ t, s }: { t: T; s: State }) {
  const data = s.proxies!;
  const usage = proxyUsage(credentialsOf(s.status));
  const locale = localeTag(s.lang);
  const body = useRef<HTMLTableSectionElement>(null);
  const ids = useRef<string[]>([]);
  ids.current = data.proxies.map((p) => p.id);
  // The drag lives in this table only; the order is saved when it ends.
  const dragRef = useRef<Drag | null>(null);
  const [drag, setDragState] = useState<Drag | null>(null);
  const setDrag = (next: Drag | null) => {
    dragRef.current = next;
    setDragState(next);
  };
  const draggingId = drag ? drag.id : null;

  useEffect(() => {
    if (!draggingId) return;
    const middles = () =>
      [...(body.current ? body.current.querySelectorAll<HTMLTableRowElement>("tr[data-proxy-id]") : [])].map((row) => {
        const box = row.getBoundingClientRect();
        return box.top + box.height / 2;
      });
    const onMove = (event: PointerEvent) => {
      const current = dragRef.current;
      if (!current) return;
      event.preventDefault();
      const to = dropIndex(middles(), event.clientY, current.from);
      if (to !== current.to) setDrag({ ...current, to });
    };
    const onUp = () => {
      const current = dragRef.current;
      setDrag(null);
      if (!current) return;
      const from = ids.current.indexOf(current.id);
      if (from >= 0 && current.to !== from) void store.reorderProxies(moveItem(ids.current, from, current.to));
    };
    const onCancel = () => setDrag(null);
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setDrag(null);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onCancel);
    window.addEventListener("keydown", onKey);
    document.body.classList.add("proxy-dragging");
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onCancel);
      window.removeEventListener("keydown", onKey);
      document.body.classList.remove("proxy-dragging");
    };
  }, [draggingId]);

  const onGrab = (event: ReactPointerEvent<HTMLButtonElement>, id: string) => {
    if (event.button !== 0 || data.proxies.length < 2) return;
    event.preventDefault();
    const from = ids.current.indexOf(id);
    if (from >= 0) setDrag({ id, from, to: from });
  };
  const markOf = (index: number): DropMark => {
    if (!drag || drag.to === drag.from || index !== drag.to) return "";
    return drag.to < drag.from ? "drop-before" : "drop-after";
  };
  return (
    <div className="table-wrap">
      <table className="proxies-table">
        <thead>
          <tr>{COLUMNS.map((key) => <th key={key} className={"col-" + key}>{t("proxies.col." + key)}</th>)}</tr>
        </thead>
        <tbody ref={body}>
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
          {data.proxies.map((proxy, index) => (
            <ProxyRow
              key={proxy.id}
              t={t}
              s={s}
              proxy={proxy}
              used={usage.byId.get(proxy.id) || 0}
              locale={locale}
              dragging={draggingId === proxy.id}
              mark={markOf(index)}
              onGrab={onGrab}
            />
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
