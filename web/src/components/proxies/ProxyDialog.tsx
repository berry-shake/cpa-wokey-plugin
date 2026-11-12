import { useEffect, useLayoutEffect, useRef } from "react";
import type { T } from "../../i18n";
import { newProxyDialog, type ProxyDialogState } from "../../lib/proxies";
import { store, type State } from "../../state/store";

/**
 * Add or edit a proxy. Like the key editor it stays mounted: a new seq
 * opens it and Esc closes it natively.
 */
export function ProxyDialog({ t, s }: { t: T; s: State }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const nameInput = useRef<HTMLInputElement>(null);
  // While closed it keeps showing the last form, minus a typed address
  // (which may carry a password).
  const last = useRef<ProxyDialogState | null>(null);
  if (s.proxyDialog) last.current = s.proxyDialog;
  else if (last.current && last.current.url) last.current = { ...last.current, url: "" };
  const d = s.proxyDialog || last.current || newProxyDialog(0, null);
  const seq = s.proxyDialog ? s.proxyDialog.seq : 0;

  useLayoutEffect(() => {
    const box = dialog.current;
    if (!box) return;
    if (!seq) {
      if (box.open) box.close();
      return;
    }
    if (!box.open) box.showModal();
    nameInput.current?.focus();
  }, [seq]);

  useEffect(() => {
    const box = dialog.current;
    if (!box) return;
    const onClose = () => {
      if (!box.open) store.closeProxyDialog();
    };
    box.addEventListener("close", onClose);
    return () => box.removeEventListener("close", onClose);
  }, []);

  const adding = d.mode === "add";
  return (
    <dialog className="modal modal-sm" id="proxy-dialog" aria-labelledby="proxy-dialog-title" ref={dialog}>
      <form
        className="modal-form"
        id="proxy-form"
        autoComplete="off"
        onSubmit={(event) => {
          event.preventDefault();
          void store.saveProxyDialog();
        }}
      >
        <div className="modal-head">
          <div>
            <h2 id="proxy-dialog-title">{adding ? t("proxies.dialog.addTitle") : t("proxies.dialog.editTitle", { name: d.title })}</h2>
            <p className="sub">{t("proxies.dialog.hint")}</p>
          </div>
          <button className="icon-btn" type="button" aria-label={t("form.close")} title={t("form.close")} onClick={() => store.closeProxyDialog()}>
            ×
          </button>
        </div>
        <div className="modal-body">
          <div className="form-stack">
            <div className="field">
              <label htmlFor="p-name">{t("proxies.dialog.name")}</label>
              <input
                ref={nameInput}
                className="input"
                id="p-name"
                type="text"
                maxLength={80}
                placeholder={t("proxies.dialog.namePlaceholder")}
                value={d.name}
                onChange={(event) => store.updateProxyDialog({ name: event.target.value })}
              />
            </div>
            <div className="field">
              <label htmlFor="p-url">{t("proxies.dialog.url")}</label>
              <input
                className="input mono"
                id="p-url"
                type="text"
                spellCheck={false}
                placeholder={adding ? "socks5://user:pass@host:1080" : d.origUrl}
                value={d.url}
                onChange={(event) => store.updateProxyDialog({ url: event.target.value })}
              />
              {adding ? null : <span className="help">{t("proxies.dialog.urlKeep", { url: d.origUrl })}</span>}
              {!adding && d.usedBy ? <span className="help warn-text" id="p-url-used">{t("proxies.dialog.usedBy", { n: d.usedBy })}</span> : null}
            </div>
          </div>
        </div>
        <div className="modal-foot">
          <span className={"form-error" + (d.error ? "" : " hidden")} role="alert">
            {d.error ? t(d.error.key, d.error.params) : ""}
          </span>
          <button className="btn btn-secondary" type="button" onClick={() => store.closeProxyDialog()}>
            {t("form.cancel")}
          </button>
          <button className="btn btn-primary" type="submit" id="proxy-save" disabled={!!s.busy["proxy-save"]}>
            {adding ? t("proxies.dialog.add") : t("form.save")}
          </button>
        </div>
      </form>
    </dialog>
  );
}
