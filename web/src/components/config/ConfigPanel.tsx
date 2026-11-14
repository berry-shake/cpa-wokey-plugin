import type { StatusView } from "../../api/types";
import type { T } from "../../i18n";
import { forceState } from "../../lib/credentials";
import { yamlScalar } from "../../lib/format";
import { store, type State } from "../../state/store";

interface KV {
  label: string;
  value: string;
  mono?: boolean;
  sub?: string;
  subClass?: string;
}

function KVList({ id, rows }: { id: string; rows: KV[] }) {
  return (
    <dl className="kv" id={id}>
      {rows.map((row) => [
        <dt key={row.label + ":dt"}>{row.label}</dt>,
        <dd key={row.label + ":dd"}>
          <span className={row.mono ? "mono" : undefined}>{row.value}</span>
          {row.sub ? <div className={"sub" + (row.subClass ? " " + row.subClass : "")}>{row.sub}</div> : null}
        </dd>,
      ])}
    </dl>
  );
}

export function configRows(t: T, data: StatusView): KV[] {
  const cfg = data.config || ({} as Partial<StatusView["config"]>);
  const minutes = Number(cfg.models_refresh_minutes) || 0;
  const prefix = cfg.default_prefix || "";
  return [
    { label: t("cfg.version"), value: data.version || "—" },
    { label: t("cfg.baseURL"), value: cfg.base_url || "—", mono: true },
    { label: t("cfg.prefix"), value: prefix || t("prefix.none"), mono: !!prefix, sub: t("cfg.prefixSub") },
    { label: t("cfg.refresh"), value: minutes > 0 ? t("cfg.refreshValue", { n: minutes }) : t("cfg.refreshOff") },
    { label: t("cfg.timeout"), value: cfg.request_timeout_seconds ? t("cfg.seconds", { n: cfg.request_timeout_seconds }) : "—" },
    { label: t("cfg.streamIdle"), value: cfg.stream_idle_timeout_seconds ? t("cfg.seconds", { n: cfg.stream_idle_timeout_seconds }) : "—" },
  ];
}

/** The plugin's settings as they would appear in the CPA config file. */
export function configYAML(data: StatusView): string {
  const cfg = data.config || ({} as Partial<StatusView["config"]>);
  return [
    "plugins:",
    "  configs:",
    "    wokey:",
    "      base-url: " + yamlScalar(cfg.base_url || ""),
    "      model-prefix: " + yamlScalar(cfg.default_prefix || ""),
    "      models-refresh-minutes: " + (Number(cfg.models_refresh_minutes) || 0),
    "      request-timeout-seconds: " + (Number(cfg.request_timeout_seconds) || 0),
    "      stream-idle-timeout-seconds: " + (Number(cfg.stream_idle_timeout_seconds) || 0),
  ].join("\n");
}

function routingRows(t: T, data: StatusView): KV[] {
  const cfg = data.config || ({} as Partial<StatusView["config"]>);
  const prefix = cfg.default_prefix || "";
  const force = forceState(cfg);
  const rows: KV[] = [
    { label: t("routing.force"), value: t("routing.force" + force), sub: t("routing.force" + force + "Sub"), subClass: force === "Off" ? "warn-text" : "" },
  ];
  if (prefix) {
    rows.push({ label: t("routing.names"), value: prefix + "/<model>", mono: true, sub: force === "On" ? "" : t("routing.namesBoth", { bare: "<model>" }) });
  } else {
    rows.push({ label: t("routing.names"), value: "<model>", mono: true, sub: t("routing.namesBare") });
  }
  rows.push({ label: t("routing.proxy"), value: cfg.global_proxy || t("routing.proxyNone"), mono: !!cfg.global_proxy });
  return rows;
}

export function ConfigPanel({ t, s }: { t: T; s: State }) {
  const data = s.status;
  const cfg = data ? data.config || ({} as Partial<StatusView["config"]>) : null;
  const force = cfg ? forceState(cfg) : "Unknown";
  // The warning matters only while some Wokey key registers a prefix.
  const creds = data && Array.isArray(data.credentials) ? data.credentials : [];
  const prefixed = !!(cfg && cfg.default_prefix) || creds.some((c) => c.effective_prefix);
  const showNotice = !!data && force === "Off" && prefixed;
  return (
    <section className="panel" role="tabpanel" id="panel-config" aria-labelledby="tab-config" hidden={s.tab !== "config"}>
      <div className="config-grid">
        <section className="card">
          <div className="card-header">
            <div className="card-title">
              <h2>{t("config.title")}</h2>
              <span className="hint mono">plugins.configs.wokey</span>
            </div>
          </div>
          <div className="card-body">
            <KVList id="config-list" rows={data ? configRows(t, data) : []} />
            <details className="help">
              <summary>{t("config.yaml")}</summary>
              <pre className="code" id="config-yaml">{data ? configYAML(data) : ""}</pre>
            </details>
            <p className="config-note">{t("config.note")}</p>
          </div>
        </section>
        <div className="config-side">
          <section className="card">
            <div className="card-header">
              <div className="card-title">
                <h2>{t("routing.title")}</h2>
                <span className="hint">{t("routing.hint")}</span>
              </div>
              <span className={"badge" + (data ? " " + (force === "On" ? "ok" : "warn") : "")} id="force-badge" hidden={!data || force === "Unknown"}>
                {data ? t("routing.force" + force) : ""}
              </span>
            </div>
            <div className="card-body">
              <KVList id="routing-list" rows={data ? routingRows(t, data) : []} />
              <div className="notice stack" id="force-notice" data-state="warn" hidden={!showNotice}>
                <span className="notice-text" id="force-notice-text">{data ? t("routing.offNotice") : ""}</span>
                <pre className="code">{"routing:\n  force-model-prefix: true"}</pre>
              </div>
              <details className="help">
                <summary>{t("routing.helpTitle")}</summary>
                <p>{t("routing.help1")}</p>
                <p>{t("routing.help2")}</p>
                <p>{t("routing.help3")}</p>
              </details>
            </div>
          </section>
          <section className="card">
            <div className="card-header">
              <div className="card-title">
                <h2>{t("auth.title")}</h2>
                <span className="hint" id="auth-source">{s.keySource ? t(s.keySource === "panel" ? "auth.panel" : "auth.manual") : ""}</span>
              </div>
              <button id="switch-key" className="btn btn-secondary btn-sm" type="button" onClick={() => store.showLogin()}>
                {t("auth.switch")}
              </button>
            </div>
          </section>
        </div>
      </div>
    </section>
  );
}
