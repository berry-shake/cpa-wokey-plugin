import type { T } from "../i18n";
import { store, type State } from "../state/store";

export function LoginCard({ t, s }: { t: T; s: State }) {
  return (
    <section className={"card" + (s.view === "login" ? "" : " hidden")} id="login-card">
      <div className="card-header">
        <h2>{t("login.title")}</h2>
      </div>
      <div className="card-body">
        <form
          className="login-form"
          id="login-form"
          onSubmit={(event) => {
            event.preventDefault();
            void store.login();
          }}
        >
          <input
            className="input"
            id="login-key"
            type="password"
            autoComplete="off"
            spellCheck={false}
            placeholder={t("login.placeholder")}
            value={s.loginInput}
            onChange={(event) => store.setLoginInput(event.target.value)}
          />
          <button id="login-button" className="btn btn-primary" type="submit" disabled={!!s.busy.login}>{t("login.submit")}</button>
        </form>
        <p className="login-hint">{t("login.hint")}</p>
        <p className={"login-error" + (s.loginError ? "" : " hidden")} id="login-error" role="alert">
          {s.loginError ? t(s.loginError.key, s.loginError.params) : ""}
        </p>
      </div>
    </section>
  );
}
