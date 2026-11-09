import { useSyncExternalStore } from "react";
import { createApi, errorMessage, errorStatus, isAuthError } from "../api/client";
import { readPanelKey } from "../api/panelKey";
import type { CredentialView, LogPage, ModelsResponse, StatusView } from "../api/types";
import { initialLang, saveLang, translate, type Lang, type Params } from "../i18n";
import {
  credentialsOf, defaultPrefix, displayName, quotaQueue, statusHealth,
  type Health, type QuotaEntry, type QuotaMap,
} from "../lib/credentials";
import {
  addSelected, buildSaveBody, newEditor, pickerRows, toggleSelected, type EditorState,
} from "../lib/editor";
import {
  customRangeError, isFiltered, lastPageOffset, logRangeParams, LOG_RANGE_KEY, LOG_SIZE_KEY,
  offsetForSize, parseLogSize, restoreLogRange, type LogRange,
} from "../lib/logs";

export const TABS = ["logs", "keys", "models", "config"] as const;
export type Tab = (typeof TABS)[number];
export const TAB_KEY = "cliproxy-wokey-tab";
const REFRESH_MS = 30000;
const QUOTA_CONCURRENCY = 3;

export interface Message {
  key: string;
  params: Params | null;
}

export interface Notice extends Message {
  state: "ok" | "warn" | "error";
}

export interface LogsState {
  page: LogPage | null;
  offset: number;
  size: number;
  status: string;
  search: string;
  range: LogRange;
  /** Applied custom bounds; the inputs only count once applied. */
  start: string;
  end: string;
  draftStart: string;
  draftEnd: string;
  rangeError: string;
  clearArmed: boolean;
  /** Whether the shown page was narrowed by range, status or search. */
  filtered: boolean;
}

export interface State {
  lang: Lang;
  tab: Tab;
  /** "init" until the first status decides between the login card and the app. */
  view: "init" | "login" | "app";
  loginInput: string;
  loginError: Message | null;
  keySource: "" | "panel" | "manual";
  health: Health;
  status: StatusView | null;
  loadedAt: number | null;
  notice: Notice | null;
  toast: Message | null;
  quota: QuotaMap;
  catalog: ModelsResponse | null;
  catalogError: string | null;
  catalogLoading: boolean;
  catalogFilter: string;
  keysFilter: string;
  armedDelete: string | null;
  /** Buttons waiting on a request: refresh, catalog, quota, login, save, logs, toggle:<name>, delete:<name>. */
  busy: Record<string, true>;
  /** The open editor; null while the dialog is closed. */
  editor: EditorState | null;
  logs: LogsState;
  /** Bumped when the custom range opens so its start input takes focus. */
  focusRangeStart: number;
}

function readStorage(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* ignore */
  }
}

export function isTab(value: unknown): value is Tab {
  return (TABS as readonly unknown[]).includes(value);
}

export function initialState(now = Date.now()): State {
  const savedTab = readStorage(TAB_KEY);
  const range = restoreLogRange(readStorage(LOG_RANGE_KEY), now);
  return {
    lang: initialLang(),
    tab: isTab(savedTab) ? savedTab : TABS[0],
    view: "init",
    loginInput: "",
    loginError: null,
    keySource: "",
    health: { state: "unknown", key: "health.unknown", params: null },
    status: null,
    loadedAt: null,
    notice: null,
    toast: null,
    quota: {},
    catalog: null,
    catalogError: null,
    catalogLoading: false,
    catalogFilter: "",
    keysFilter: "",
    armedDelete: null,
    busy: {},
    editor: null,
    logs: {
      page: null,
      offset: 0,
      size: parseLogSize(readStorage(LOG_SIZE_KEY)),
      status: "all",
      search: "",
      range: range.range,
      start: range.start,
      end: range.end,
      draftStart: range.start,
      draftEnd: range.end,
      rangeError: "",
      clearArmed: false,
      filtered: false,
    },
    focusRangeStart: 0,
  };
}

/**
 * All page state and the actions that change it. State is replaced, never
 * mutated, so React can subscribe with useSyncExternalStore. The management
 * key lives only here; a reload starts over.
 */
export class Store {
  private state: State;
  private readonly listeners = new Set<() => void>();
  private key = "";
  private readonly api = createApi(() => this.key);
  private refreshTimer: ReturnType<typeof setInterval> | null = null;
  private toastTimer: ReturnType<typeof setTimeout> | null = null;
  private deleteTimer: ReturnType<typeof setTimeout> | null = null;
  private clearTimer: ReturnType<typeof setTimeout> | null = null;
  private searchTimer: ReturnType<typeof setTimeout> | null = null;
  private logSeq = 0;
  private editorSeq = 0;

  constructor(state: State = initialState()) {
    this.state = state;
  }

  getState = (): State => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  t = (key: string, params?: Params | null): string => translate(this.state.lang, key, params);

  private set(patch: Partial<State> | ((s: State) => Partial<State>)): void {
    const next = typeof patch === "function" ? patch(this.state) : patch;
    this.state = { ...this.state, ...next };
    for (const listener of this.listeners) listener();
  }

  private setLogs(patch: Partial<LogsState>): void {
    this.set((s) => ({ logs: { ...s.logs, ...patch } }));
  }

  private setBusy(name: string, on: boolean): void {
    this.set((s) => {
      const busy = { ...s.busy };
      if (on) busy[name] = true;
      else delete busy[name];
      return { busy };
    });
  }

  private setQuota(authIndex: string, entry: QuotaEntry | undefined): void {
    this.set((s) => {
      const quota = { ...s.quota };
      if (entry) quota[authIndex] = entry;
      else delete quota[authIndex];
      return { quota };
    });
  }

  // ---- messages ----

  setLang(lang: Lang): void {
    saveLang(lang);
    this.set({ lang });
  }

  private setNotice(state: Notice["state"] | null, key = "", params?: Params | null): void {
    this.set({ notice: state ? { state, key, params: params || null } : null });
  }

  dismissNotice(): void {
    this.setNotice(null);
  }

  private showToast(key: string, params?: Params | null): void {
    this.set({ toast: { key, params: params || null } });
    if (this.toastTimer) clearTimeout(this.toastTimer);
    this.toastTimer = setTimeout(() => {
      this.toastTimer = null;
      this.set({ toast: null });
    }, 3500);
  }

  // Successes are a short toast; warnings and errors stay in the notice bar
  // until dismissed.
  private notify(state: Notice["state"], key: string, params?: Params | null): void {
    if (state === "ok") {
      this.setNotice(null);
      this.showToast(key, params);
    } else {
      this.setNotice(state, key, params);
    }
  }

  private handleError(error: unknown, key?: string): void {
    if (isAuthError(error)) {
      this.showLogin("login.expired", errorMessage(error));
      return;
    }
    this.setNotice("error", key || "log.actionFailed", { msg: errorMessage(error) });
  }

  // ---- tabs ----

  selectTab(name: string): void {
    const tab = isTab(name) ? name : TABS[0];
    writeStorage(TAB_KEY, tab);
    this.set({ tab });
    this.ensureTabData();
  }

  // The model tab loads the full catalog lazily, and only once signed in.
  private ensureTabData(): void {
    const s = this.state;
    if (this.key && s.tab === "logs") this.loadLogs(true).catch(() => {});
    if (this.key && s.tab === "models" && !s.catalog && !s.catalogLoading) this.loadCatalog(false).catch(() => {});
  }

  // ---- sign-in ----

  setLoginInput(value: string): void {
    this.set({ loginInput: value });
  }

  showLogin(errorKey?: string, msg?: string): void {
    this.key = "";
    this.stopAutoRefresh();
    this.closeEditor();
    this.set({
      view: "login",
      loginError: errorKey ? { key: errorKey, params: { msg } } : null,
      health: { state: "unknown", key: "health.locked", params: null },
    });
  }

  private showContent(): void {
    this.set({ view: "app", loginError: null });
    this.startAutoRefresh();
    this.ensureTabData();
  }

  private startAutoRefresh(): void {
    this.stopAutoRefresh();
    this.refreshTimer = setInterval(() => {
      if (document.visibilityState === "visible") {
        void this.refresh(true);
        if (this.state.tab === "logs") this.loadLogs(true).catch(() => {});
      }
    }, REFRESH_MS);
  }

  private stopAutoRefresh(): void {
    if (this.refreshTimer) clearInterval(this.refreshTimer);
    this.refreshTimer = null;
  }

  private enter(data: StatusView): void {
    this.showContent();
    this.applyStatus(data);
    void this.loadAllQuotas(false);
  }

  async login(): Promise<void> {
    const key = this.state.loginInput.trim();
    if (!key) return;
    this.key = key;
    this.set({ keySource: "manual" });
    this.setBusy("login", true);
    try {
      const data = await this.api.status();
      this.set({ loginInput: "" });
      this.enter(data);
    } catch (error) {
      if (isAuthError(error) || !errorStatus(error)) {
        this.showLogin("login.failed", errorMessage(error));
      } else {
        // CPA accepted the key; only the plugin failed, so show the error in place.
        this.set({ loginInput: "" });
        this.showContent();
        this.set({ health: { state: "error", key: "health.error", params: null } });
        this.setNotice("error", "log.loadFailed", { msg: errorMessage(error) });
      }
    } finally {
      this.setBusy("login", false);
    }
  }

  // Load once on open. Without a remembered panel key, or when CPA rejects
  // it, ask for the key instead.
  async start(panelKey: string = readPanelKey()): Promise<void> {
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible" && this.key && this.refreshTimer) void this.refresh(true);
    });
    this.key = panelKey;
    if (!this.key) {
      this.showLogin();
      return;
    }
    this.set({ keySource: "panel" });
    try {
      this.enter(await this.api.status());
    } catch (error) {
      if (isAuthError(error)) {
        this.showLogin();
        return;
      }
      this.showContent();
      this.set({ health: { state: "error", key: "health.error", params: null } });
      this.setNotice("error", "log.loadFailed", { msg: errorMessage(error) });
    }
  }

  // ---- status, balances and catalog ----

  private applyStatus(data: StatusView): void {
    this.set({ status: data, loadedAt: Date.now(), health: statusHealth(data) });
    // The loaded model list follows the catalog the plugin currently serves.
    const s = this.state;
    const loaded = s.catalog && s.catalog.catalog;
    if (loaded && data.catalog && data.catalog.revision && loaded.revision !== data.catalog.revision && !s.catalogLoading) {
      this.loadCatalog(false).catch(() => {});
    }
  }

  async refresh(silent: boolean): Promise<void> {
    if (!silent) this.setBusy("refresh", true);
    try {
      this.applyStatus(await this.api.status());
      if (!silent) this.setNotice(null);
    } catch (error) {
      if (isAuthError(error)) {
        this.showLogin("login.expired", errorMessage(error));
        return;
      }
      this.set({ health: { state: "error", key: "health.error", params: null } });
      this.setNotice("error", "log.loadFailed", { msg: errorMessage(error) });
    } finally {
      if (!silent) this.setBusy("refresh", false);
    }
  }

  // Keeps the last result on screen while it reloads.
  async loadQuota(authIndex: string): Promise<void> {
    if (!authIndex) return;
    const prev = this.state.quota[authIndex];
    this.setQuota(authIndex, { state: "loading", data: prev && prev.data });
    try {
      const data = await this.api.quota(authIndex);
      this.setQuota(authIndex, { state: "ok", data });
    } catch (error) {
      if (isAuthError(error)) {
        this.showLogin("login.expired", errorMessage(error));
        return;
      }
      this.setQuota(authIndex, { state: "error", error: errorMessage(error), data: prev && prev.data });
    }
  }

  // Loads balances a few at a time.
  private async loadAllQuotas(force: boolean): Promise<void> {
    const queue = quotaQueue(credentialsOf(this.state.status), this.state.quota, force);
    const workers: Promise<void>[] = [];
    for (let i = 0; i < QUOTA_CONCURRENCY; i++) {
      workers.push((async () => {
        while (queue.length && this.key) await this.loadQuota(queue.shift() as string);
      })());
    }
    await Promise.all(workers);
  }

  async refreshQuotas(): Promise<void> {
    this.setBusy("quota", true);
    try {
      await this.loadAllQuotas(true);
    } finally {
      this.setBusy("quota", false);
    }
  }

  private async loadCatalog(force: boolean): Promise<void> {
    this.set({ catalogLoading: true });
    try {
      const catalog = force ? await this.api.refreshModels() : await this.api.models();
      this.set({ catalog, catalogError: null });
    } catch (error) {
      if (isAuthError(error)) {
        this.showLogin("login.expired", errorMessage(error));
        throw error;
      }
      this.set({ catalogError: errorMessage(error) });
      if (force) throw error;
    } finally {
      this.set({ catalogLoading: false });
    }
  }

  async reloadCatalog(): Promise<void> {
    this.setBusy("catalog", true);
    try {
      await this.loadCatalog(true);
      const catalog = this.state.catalog;
      this.notify("ok", "log.catalogReloaded", { count: catalog && catalog.models ? catalog.models.length : 0 });
      await this.refresh(true);
    } catch (error) {
      this.handleError(error, "log.loadFailed");
    } finally {
      this.setBusy("catalog", false);
    }
  }

  setCatalogFilter(value: string): void {
    this.set({ catalogFilter: value });
  }

  setKeysFilter(value: string): void {
    this.set({ keysFilter: value });
  }

  // ---- credential actions ----

  async toggleDisabled(cred: CredentialView): Promise<void> {
    if (!cred.name) return;
    const busy = "toggle:" + cred.name;
    this.setBusy(busy, true);
    try {
      await this.api.setDisabled(cred.name, cred.auth_index, !cred.disabled);
      this.notify("ok", cred.disabled ? "log.enabled" : "log.disabled", { name: displayName(cred) });
      await this.refresh(true);
    } catch (error) {
      this.handleError(error);
    } finally {
      this.setBusy(busy, false);
    }
  }

  private disarmDelete(): void {
    if (this.deleteTimer) clearTimeout(this.deleteTimer);
    this.deleteTimer = null;
    if (this.state.armedDelete !== null) this.set({ armedDelete: null });
  }

  // Deleting is a two-click action: confirm() may be blocked inside the
  // management panel's iframe.
  async deleteCredential(cred: CredentialView): Promise<void> {
    if (!cred.name) return;
    if (this.state.armedDelete !== cred.name) {
      this.disarmDelete();
      this.set({ armedDelete: cred.name });
      this.deleteTimer = setTimeout(() => this.disarmDelete(), 4000);
      return;
    }
    this.disarmDelete();
    const busy = "delete:" + cred.name;
    this.setBusy(busy, true);
    try {
      await this.api.deleteAuthFile(cred.name);
      this.setQuota(cred.auth_index, undefined);
      const ed = this.state.editor;
      if (ed && ed.authIndex === cred.auth_index) this.closeEditor();
      this.notify("ok", "log.deleted", { name: displayName(cred) });
      await this.refresh(true);
    } catch (error) {
      this.handleError(error);
    } finally {
      this.setBusy(busy, false);
    }
  }

  // ---- editor ----

  openEditor(mode: "add" | "edit", cred?: CredentialView): void {
    const s = this.state;
    this.set({ editor: newEditor(++this.editorSeq, mode, cred || null, defaultPrefix(s.status)) });
    if (!s.catalog && !s.catalogLoading) this.loadCatalog(false).catch(() => {});
  }

  closeEditor(): void {
    if (this.state.editor) this.set({ editor: null });
  }

  /** Field edits; ignored once the dialog closed. */
  updateEditor(patch: Partial<EditorState>): void {
    const ed = this.state.editor;
    if (ed) this.set({ editor: { ...ed, ...patch } });
  }

  setPrefixDefault(checked: boolean): void {
    const ed = this.state.editor;
    if (!ed) return;
    if (checked) {
      this.updateEditor({ prefixDefault: true });
      return;
    }
    // Unchecking restores a saved custom prefix; otherwise the input keeps
    // the default it was showing.
    const orig = ed.original;
    const prefix = orig && orig.prefix !== null && orig.prefix !== undefined ? orig.prefix : defaultPrefix(this.state.status);
    this.updateEditor({ prefixDefault: false, prefix });
  }

  setIssuerClear(checked: boolean): void {
    this.updateEditor(checked ? { issuerClear: true, issuer: "" } : { issuerClear: false });
  }

  pickModel(id: string, on: boolean): void {
    const ed = this.state.editor;
    if (ed) this.updateEditor({ selected: toggleSelected(ed.selected, id, on) });
  }

  selectShownModels(): void {
    const ed = this.state.editor;
    const catalog = this.state.catalog;
    if (!ed || !catalog) return;
    const models = Array.isArray(catalog.models) ? catalog.models : [];
    const shown = pickerRows(models, ed.selected, ed.filter).map((r) => r.id);
    this.updateEditor({ selected: addSelected(ed.selected, shown) });
  }

  clearPickedModels(): void {
    this.updateEditor({ selected: [] });
  }

  async saveEditor(): Promise<void> {
    const ed = this.state.editor;
    if (!ed) return;
    const adding = ed.mode === "add";
    const result = buildSaveBody(ed);
    if ("error" in result) {
      this.updateEditor({ error: { key: result.error, params: null } });
      return;
    }
    const body = result.body;
    this.updateEditor({ error: null });
    this.setBusy("save", true);
    try {
      const data = await this.api.saveCredential(adding, body);
      const cred = data && data.credential;
      const name = cred ? displayName(cred) : ed.title || "";
      this.closeEditor();
      if (data && Array.isArray(data.unknown_models) && data.unknown_models.length) {
        this.setNotice("warn", "log.unknownModels", { name, list: data.unknown_models.join(", ") });
      } else {
        this.notify("ok", adding ? "log.added" : "log.updated", { name });
      }
      await this.refresh(true);
      if (cred && cred.auth_index && (adding || body.issuer_token !== undefined)) void this.loadQuota(cred.auth_index);
    } catch (error) {
      if (isAuthError(error)) {
        this.showLogin("login.expired", errorMessage(error));
        return;
      }
      const current = this.state.editor;
      if (current && current.seq === ed.seq) {
        this.updateEditor({ error: { key: "log.actionFailed", params: { msg: errorMessage(error) } } });
      }
    } finally {
      this.setBusy("save", false);
    }
  }

  // ---- request log ----

  async loadLogs(silent: boolean): Promise<void> {
    const logs = this.state.logs;
    const range = logRangeParams(logs, Date.now());
    if (!range) return;
    const seq = ++this.logSeq;
    const params = new URLSearchParams({
      limit: String(logs.size),
      offset: String(logs.offset),
      status: logs.status,
      search: logs.search.trim(),
      ...range,
    });
    if (!silent) this.setBusy("logs", true);
    try {
      const page = await this.api.logs(params);
      if (seq !== this.logSeq) return;
      const now = this.state.logs;
      if (!(page.items || []).length && page.total > 0 && now.offset > 0) {
        // The page went past the end (log trimmed or filter changed): show the last page.
        this.setLogs({ offset: lastPageOffset(page.total, now.size) });
        this.loadLogs(silent).catch(() => {});
        return;
      }
      this.setLogs({ page, filtered: isFiltered(now.range, now.status, now.search) });
    } catch (error) {
      this.handleError(error, "log.loadFailed");
    } finally {
      if (!silent) this.setBusy("logs", false);
    }
  }

  private reloadLogsFromStart(silent: boolean): void {
    this.setLogs({ offset: 0 });
    this.loadLogs(silent).catch(() => {});
  }

  setLogsSearch(value: string): void {
    this.setLogs({ search: value });
    if (this.searchTimer) clearTimeout(this.searchTimer);
    this.searchTimer = setTimeout(() => {
      this.searchTimer = null;
      this.reloadLogsFromStart(true);
    }, 250);
  }

  setLogsStatus(value: string): void {
    this.setLogs({ status: value });
    this.reloadLogsFromStart(true);
  }

  setLogsDraft(which: "start" | "end", value: string): void {
    this.setLogs(which === "start" ? { draftStart: value } : { draftEnd: value });
  }

  private rememberLogRange(): void {
    const { range, start, end } = this.state.logs;
    writeStorage(LOG_RANGE_KEY, JSON.stringify({ range, start, end }));
  }

  /** Takes the inputs as the custom range when they are valid. */
  private applyCustomRange(): boolean {
    const { draftStart, draftEnd } = this.state.logs;
    const error = customRangeError(draftStart, draftEnd);
    this.setLogs(error ? { rangeError: error } : { rangeError: "", start: draftStart, end: draftEnd });
    return !error;
  }

  selectLogRange(range: LogRange): void {
    if (range === this.state.logs.range) return;
    this.setLogs({ range, rangeError: "" });
    if (range === "custom") {
      if (!this.applyCustomRange()) return;
      this.set((s) => ({ focusRangeStart: s.focusRangeStart + 1 }));
    }
    this.rememberLogRange();
    this.reloadLogsFromStart(true);
  }

  applyLogRange(): void {
    if (!this.applyCustomRange()) return;
    this.rememberLogRange();
    this.reloadLogsFromStart(false);
  }

  setLogsSize(size: number): void {
    const page = this.state.logs.page;
    writeStorage(LOG_SIZE_KEY, String(size));
    this.setLogs({ size, offset: offsetForSize(page ? page.offset : 0, size) });
    this.loadLogs(true).catch(() => {});
  }

  /** Shows the zero-based page n; the caller checks the bounds. */
  goLogsPage(n: number): void {
    this.setLogs({ offset: n * this.state.logs.size });
    this.loadLogs(true).catch(() => {});
  }

  async clearLogs(): Promise<void> {
    if (!this.state.logs.clearArmed) {
      this.setLogs({ clearArmed: true });
      this.clearTimer = setTimeout(() => {
        this.clearTimer = null;
        this.setLogs({ clearArmed: false });
      }, 3000);
      return;
    }
    if (this.clearTimer) clearTimeout(this.clearTimer);
    this.clearTimer = null;
    this.setLogs({ clearArmed: false });
    try {
      await this.api.clearLogs();
      this.setLogs({ offset: 0 });
      this.setNotice("ok", "logs.cleared");
      await this.loadLogs(true);
    } catch (error) {
      this.handleError(error);
    }
  }
}

export const store = new Store();

export function useAppState(): State {
  return useSyncExternalStore(store.subscribe, store.getState);
}
