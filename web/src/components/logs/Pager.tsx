import { Fragment, useEffect, useRef } from "react";
import type { LogPage } from "../../api/types";
import type { T } from "../../i18n";
import { jumpTarget, LOG_SIZES, pagerNumbers, pagerView } from "../../lib/logs";
import { store } from "../../state/store";

// Each loaded page gets its own jump box, so a reload starts it over.
const pageIDs = new WeakMap<LogPage, number>();
let nextPageID = 0;
function pageID(page: LogPage): number {
  let id = pageIDs.get(page);
  if (id === undefined) {
    id = ++nextPageID;
    pageIDs.set(page, id);
  }
  return id;
}

function JumpBox({ t, current, pages, go }: { t: T; current: number; pages: number; go: (n: number) => void }) {
  const input = useRef<HTMLInputElement>(null);
  const jumped = useRef(false); // Enter fires both keydown and change.
  const jumpTo = () => {
    const box = input.current;
    if (!box || jumped.current) return;
    const target = jumpTarget(box.value, pages);
    if (target === null) {
      box.value = String(current + 1);
      return;
    }
    box.value = String(target);
    if (target - 1 !== current) {
      jumped.current = true;
      go(target - 1);
    }
  };
  const latest = useRef(jumpTo);
  latest.current = jumpTo;

  // The native change event (commit on blur or the spin buttons), not React's per-keystroke onChange.
  useEffect(() => {
    const box = input.current;
    if (!box) return;
    const onChange = () => latest.current();
    box.addEventListener("change", onChange);
    return () => box.removeEventListener("change", onChange);
  }, []);

  return (
    <input
      ref={input}
      className="input input-sm"
      type="number"
      min={1}
      max={pages}
      step={1}
      inputMode="numeric"
      defaultValue={current + 1}
      aria-label={t("logs.jumpTo")}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          jumpTo();
        }
      }}
    />
  );
}

export function Pager({ t, page, shown, size }: { t: T; page: LogPage; shown: number; size: number }) {
  const { pages, current } = pagerView(page.total, page.offset, size);
  const go = (n: number) => {
    if (n < 0 || n >= pages || n === current) return;
    store.goLogsPage(n);
  };
  let last = -1;
  return (
    <>
      <div className="pager-info">
        <span>{t("logs.page", { from: page.offset + 1, to: page.offset + shown, total: page.total })}</span>
        <select
          className="input input-sm"
          aria-label={t("logs.pageSize", { n: size })}
          value={size}
          onChange={(event) => store.setLogsSize(Number(event.target.value))}
        >
          {LOG_SIZES.map((n) => <option key={n} value={n}>{t("logs.pageSize", { n })}</option>)}
        </select>
      </div>
      <div className="pager-nav">
        <button className="btn btn-secondary btn-sm" type="button" disabled={current <= 0} onClick={() => go(current - 1)}>
          {"‹ " + t("logs.prev")}
        </button>
        {pagerNumbers(current, pages).map((n) => {
          const gap = n - last > 1;
          last = n;
          return (
            <Fragment key={n}>
              {gap ? <span className="pager-gap">…</span> : null}
              <button
                className="btn btn-secondary btn-sm"
                type="button"
                title={t("logs.pageN", { n: n + 1 })}
                aria-current={n === current ? "page" : undefined}
                onClick={() => go(n)}
              >
                {String(n + 1)}
              </button>
            </Fragment>
          );
        })}
        <button className="btn btn-secondary btn-sm" type="button" disabled={current >= pages - 1} onClick={() => go(current + 1)}>
          {t("logs.next") + " ›"}
        </button>
        <label className="pager-jump">
          {t("logs.jumpTo")}
          <JumpBox key={pageID(page) + ":" + size} t={t} current={current} pages={pages} go={go} />
          {t("logs.jumpUnit", { n: pages })}
        </label>
      </div>
    </>
  );
}
