import { useEffect } from "react";
import { ConfigPanel } from "./components/config/ConfigPanel";
import { EditorDialog } from "./components/editor/EditorDialog";
import { Header } from "./components/Header";
import { KeysPanel } from "./components/keys/KeysPanel";
import { LoginCard } from "./components/LoginCard";
import { LogsPanel } from "./components/logs/LogsPanel";
import { ModelsPanel } from "./components/models/ModelsPanel";
import { ProxiesPanel } from "./components/proxies/ProxiesPanel";
import { ProxyDialog } from "./components/proxies/ProxyDialog";
import { Footer, NoticeBar, Toast } from "./components/NoticeBar";
import { TabBar } from "./components/TabBar";
import { htmlLang } from "./i18n";
import { store, useAppState } from "./state/store";

/** The page's children of <main>, which index.html provides as the root. */
export function App() {
  const s = useAppState();
  const t = store.t;

  useEffect(() => {
    document.documentElement.lang = htmlLang(s.lang);
    document.title = t("title");
  }, [s.lang, t]);

  return (
    <>
      <Header t={t} lang={s.lang} health={s.health} />
      <LoginCard t={t} s={s} />
      <div className={"app" + (s.view === "app" ? "" : " hidden")}>
        <TabBar t={t} s={s} />
        <NoticeBar t={t} notice={s.notice} />
        <LogsPanel t={t} s={s} />
        <KeysPanel t={t} s={s} />
        <ProxiesPanel t={t} s={s} />
        <ModelsPanel t={t} s={s} />
        <ConfigPanel t={t} s={s} />
      </div>
      <EditorDialog t={t} s={s} />
      <ProxyDialog t={t} s={s} />
      <Toast t={t} toast={s.toast} />
      <Footer t={t} />
    </>
  );
}
