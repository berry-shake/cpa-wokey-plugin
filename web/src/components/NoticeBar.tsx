import type { T } from "../i18n";
import { store, type Notice } from "../state/store";

/** Warnings and errors that stay until dismissed. */
export function NoticeBar({ t, notice }: { t: T; notice: Notice | null }) {
  return (
    <div className={"notice" + (notice ? "" : " hidden")} id="log" role="status" aria-live="polite" data-state={notice ? notice.state : undefined}>
      <span className="notice-text" id="log-text">{notice ? t(notice.key, notice.params) : ""}</span>
      <button className="notice-close" type="button" id="log-close" aria-label={t("log.dismiss")} title={t("log.dismiss")} onClick={() => store.dismissNotice()}>
        ×
      </button>
    </div>
  );
}

export function Toast({ t, toast }: { t: T; toast: { key: string; params: Record<string, string | number | undefined> | null } | null }) {
  return (
    <div className={"toast" + (toast ? "" : " hidden")} id="toast" role="status" aria-live="polite">
      {toast ? t(toast.key, toast.params) : ""}
    </div>
  );
}

export function Footer({ t }: { t: T }) {
  return (
    <footer>
      <span>{t("footer.text")}</span>{" · "}
      <a href="https://github.com/berry-shake/cpa-wokey-plugin" target="_blank" rel="noopener noreferrer">{t("footer.source")}</a>
    </footer>
  );
}
