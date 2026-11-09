import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { localeTag, type T } from "../i18n";
import { store, TABS, type State, type Tab } from "../state/store";

const svg = {
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.7,
  strokeLinecap: "round",
  strokeLinejoin: "round",
  "aria-hidden": "true",
} as const;

const ICONS: Record<Tab, ReactNode> = {
  logs: <svg {...svg}><path d="M8 6h12M8 12h12M8 18h12" /><circle cx="4" cy="6" r="1" /><circle cx="4" cy="12" r="1" /><circle cx="4" cy="18" r="1" /></svg>,
  keys: <svg {...svg}><circle cx="8" cy="15" r="4" /><path d="m10.85 12.15 8.65-8.65M15.5 7.5l3 3M13 10l2 2" /></svg>,
  models: <svg {...svg}><path d="m12 3 9 4.5-9 4.5-9-4.5L12 3Z" /><path d="m3 12 9 4.5 9-4.5M3 16.5 12 21l9-4.5" /></svg>,
  config: <svg {...svg}><path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1" /><circle cx="15" cy="6" r="2" /><circle cx="9" cy="12" r="2" /><circle cx="17" cy="18" r="2" /></svg>,
};

function tabCount(s: State, tab: Tab): string | null {
  const data = s.status;
  if (tab === "keys") {
    const creds = data && Array.isArray(data.credentials) ? data.credentials : null;
    return creds && !data!.list_error ? String(creds.length) : "";
  }
  if (tab === "models") {
    const count = data && data.catalog ? data.catalog.count : 0;
    return count ? String(count) : "";
  }
  return null;
}

export function TabBar({ t, s }: { t: T; s: State }) {
  const buttons = useRef<Partial<Record<Tab, HTMLButtonElement | null>>>({});

  // Arrow keys, Home and End move between tabs (WAI-ARIA tabs pattern).
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const index = TABS.indexOf(s.tab);
    let next = -1;
    if (event.key === "ArrowRight") next = (index + 1) % TABS.length;
    else if (event.key === "ArrowLeft") next = (index - 1 + TABS.length) % TABS.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = TABS.length - 1;
    if (next < 0) return;
    event.preventDefault();
    store.selectTab(TABS[next]);
    buttons.current[TABS[next]]?.focus();
  };

  return (
    <div className="tabbar">
      <div className="tabs" role="tablist" id="tabs" aria-label={t("tabs.label")} onKeyDown={onKeyDown}>
        {TABS.map((tab) => {
          const active = tab === s.tab;
          const count = tabCount(s, tab);
          return (
            <button
              key={tab}
              ref={(node) => {
                buttons.current[tab] = node;
              }}
              className="tab"
              type="button"
              role="tab"
              id={"tab-" + tab}
              data-tab={tab}
              aria-controls={"panel-" + tab}
              aria-selected={active ? "true" : "false"}
              tabIndex={active ? 0 : -1}
              onClick={() => store.selectTab(tab)}
            >
              {ICONS[tab]}
              <span>{t("tab." + tab)}</span>
              {count !== null ? <span className="tab-count" id={"tab-count-" + tab}>{count}</span> : null}
            </button>
          );
        })}
      </div>
      <div className="tabbar-side">
        <span className="hint" id="checked-at">
          {s.loadedAt ? t("checkedAt", { time: new Date(s.loadedAt).toLocaleTimeString(localeTag(s.lang)) }) : ""}
        </span>
        <button id="refresh-button" className="btn btn-secondary btn-sm" type="button" disabled={!!s.busy.refresh} onClick={() => void store.refresh(false)}>
          {t("actions.refresh")}
        </button>
      </div>
    </div>
  );
}
