import type { Lang, T } from "../i18n";
import type { Health } from "../lib/credentials";
import { store } from "../state/store";

const LANGS: [Lang, string][] = [["en", "EN"], ["zh", "中文"]];

export function Header({ t, lang, health }: { t: T; lang: Lang; health: Health }) {
  return (
    <header className="hero">
      <div className="brand">
        <div className="brand-mark" aria-hidden="true"><img src="logo.svg" alt="" /></div>
        <div>
          <h1>{t("hero.title")}</h1>
          <p>{t("hero.subtitle")}</p>
        </div>
      </div>
      <div className="hero-side">
        <div className="lang-switch" role="group" aria-label="Language">
          {LANGS.map(([code, label]) => (
            <button
              key={code}
              type="button"
              data-lang={code}
              className={code === lang ? "active" : undefined}
              aria-pressed={code === lang ? "true" : "false"}
              onClick={() => store.setLang(code)}
            >
              {label}
            </button>
          ))}
        </div>
        <div className="health" id="health" data-state={health.state} role="status" aria-live="polite">
          <span className="dot" aria-hidden="true"></span>
          <span id="health-label">{t(health.key, health.params)}</span>
        </div>
      </div>
    </header>
  );
}
