import type { CredentialSaveRequest, CredentialView, ModelView } from "../api/types";
import { cleanPrefix, fileStem, fmtPrice, fmtTokens } from "./format";
import { displayName, isDirect } from "./credentials";
import type { Params, T } from "../i18n";

/** The saved values of the credential being edited. */
export interface EditorOriginal {
  label: string;
  note: string;
  proxy: string;
  proxyChoice: ProxyChoice;
  /** Name of the list proxy the credential uses, for when the list is not loaded. */
  proxyName: string;
  /** null or undefined when the credential uses the plugin default. */
  prefix: string | null | undefined;
  priority: string;
  issuer: string;
  all: boolean;
  models: string[];
}

export interface EditorState {
  /** Bumped on every open so the dialog can tell a reopen apart. */
  seq: number;
  mode: "add" | "edit";
  authIndex: string;
  name: string;
  title: string;
  original: EditorOriginal | null;
  labelPlaceholder: string;
  issuerPlaceholder: string;
  key: string;
  skipVerify: boolean;
  label: string;
  note: string;
  /** Which egress the proxy select shows. */
  proxyChoice: ProxyChoice;
  /** The custom address input; used only while proxyChoice is "custom". */
  proxy: string;
  /** The prefix input's own value; shown only while prefixDefault is off. */
  prefix: string;
  prefixDefault: boolean;
  priority: string;
  issuer: string;
  issuerClear: boolean;
  all: boolean;
  /** Selected model IDs in the order they were picked. */
  selected: string[];
  filter: string;
  error: { key: string; params: Params | null } | null;
}

/**
 * The proxy select: "" inherits CPA's global proxy, "direct" forces a direct
 * connection, "id:<id>" uses a list proxy and "custom" an address typed in.
 */
export type ProxyChoice = "" | "direct" | "custom" | `id:${string}`;

/** The select value for a saved credential. */
export function proxyChoiceOf(cred: Pick<CredentialView, "proxy_url" | "proxy_id"> | null): ProxyChoice {
  if (!cred) return "";
  if (cred.proxy_id) return `id:${cred.proxy_id}`;
  const proxy = String(cred.proxy_url || "").trim();
  if (!proxy) return "";
  return isDirect(proxy) ? "direct" : "custom";
}

export function newEditor(seq: number, mode: "add" | "edit", cred: CredentialView | null, defaultPrefix: string): EditorState {
  const editing = mode === "edit" && cred ? cred : null;
  const usesDefault = !editing || editing.prefix === null || editing.prefix === undefined;
  const proxyChoice = proxyChoiceOf(editing);
  return {
    seq,
    mode: editing ? "edit" : "add",
    authIndex: editing ? editing.auth_index : "",
    name: editing ? editing.name : "",
    title: editing ? displayName(editing) : "",
    original: editing ? {
      label: editing.label || "",
      note: editing.note || "",
      proxy: editing.proxy_url || "",
      proxyChoice,
      proxyName: editing.proxy_name || "",
      prefix: editing.prefix,
      priority: editing.priority ? String(editing.priority) : "",
      issuer: editing.issuer_token_masked || "",
      all: !!editing.all_models,
      models: Array.isArray(editing.models) ? editing.models.slice() : [],
    } : null,
    labelPlaceholder: editing ? fileStem(editing.name) : "",
    issuerPlaceholder: (editing && editing.issuer_token_masked) || "wkt_…",
    key: "",
    skipVerify: false,
    label: editing ? editing.label || "" : "",
    note: editing ? editing.note || "" : "",
    proxyChoice,
    // Only a custom address is put in the input; a list proxy is picked by id.
    proxy: editing && proxyChoice === "custom" ? editing.proxy_url || "" : "",
    prefix: usesDefault ? defaultPrefix : editing.prefix || "",
    prefixDefault: usesDefault,
    priority: editing && editing.priority ? String(editing.priority) : "",
    issuer: "",
    issuerClear: false,
    all: !editing || !!editing.all_models,
    selected: uniq(editing && !editing.all_models && Array.isArray(editing.models) ? editing.models : []),
    filter: "",
    error: null,
  };
}

function uniq(ids: string[]): string[] {
  return [...new Set(ids)];
}

/** Adds or removes one model, keeping the order the others were picked in. */
export function toggleSelected(selected: string[], id: string, on: boolean): string[] {
  if (on) return selected.includes(id) ? selected : [...selected, id];
  return selected.filter((s) => s !== id);
}

export function addSelected(selected: string[], ids: string[]): string[] {
  return uniq([...selected, ...ids]);
}

/** The prefix the form registers models under, without surrounding slashes. */
export function currentPrefix(ed: Pick<EditorState, "prefixDefault" | "prefix">, defaultPrefix: string): string {
  return cleanPrefix(ed.prefixDefault ? defaultPrefix : ed.prefix);
}

/**
 * The model registration preview. Unless CPA forces the prefix it also
 * registers the bare model ID.
 */
export function registerPreview(t: T, prefix: string, sample: string, forced: boolean): string {
  return prefix && !forced
    ? t("form.registerAsBoth", { example: prefix + "/" + sample, bare: sample })
    : t("form.registerAs", { example: (prefix ? prefix + "/" : "") + sample });
}

/** The model shown in the preview: the first one picked, else the first in the catalog. */
export function previewSample(selected: string[], models: Pick<ModelView, "id">[]): string {
  return (selected.length ? selected[0] : models[0] && models[0].id) || "model-id";
}

export function modelMeta(t: T, model: Pick<ModelView, "context_length" | "pricing">): string {
  const parts: string[] = [];
  if (model.context_length) parts.push(t("picker.ctx", { ctx: fmtTokens(model.context_length) }));
  const price = model.pricing || {};
  if (price.input !== undefined || price.output !== undefined) {
    parts.push(t("picker.price", { input: fmtPrice(price.input), output: fmtPrice(price.output) }));
  }
  return parts.join(" · ");
}

export type PickerRow = (ModelView & { missing?: false }) | { id: string; missing: true; name?: undefined; owned_by?: undefined };

/**
 * The picker's rows: selected models the catalog no longer has (sorted)
 * first, then the catalog, filtered by id, name or owner.
 */
export function pickerRows(models: ModelView[], selected: string[], rawFilter: string): PickerRow[] {
  const known = new Set(models.map((m) => m.id));
  const rows: PickerRow[] = selected.filter((id) => !known.has(id)).sort().map((id) => ({ id, missing: true as const }));
  rows.push(...models);
  const filter = rawFilter.trim().toLowerCase();
  return rows.filter((r) => !filter ||
    r.id.toLowerCase().includes(filter) ||
    String(r.name || "").toLowerCase().includes(filter) ||
    String(r.owned_by || "").toLowerCase().includes(filter));
}

export type SaveResult = { body: CredentialSaveRequest } | { error: string };

/**
 * The request body for the form: only fields that changed when editing.
 * Returns the message key of the first problem instead when the form is
 * not valid.
 */
export function buildSaveBody(ed: EditorState): SaveResult {
  const adding = ed.mode === "add";
  const body: CredentialSaveRequest = {};
  if (adding) {
    const key = ed.key.trim();
    if (!key) return { error: "form.needKey" };
    body.api_key = key;
    if (ed.skipVerify) body.skip_verify = true;
  } else {
    body.auth_index = ed.authIndex;
    body.name = ed.name;
  }
  const orig: EditorOriginal = ed.original || { label: "", note: "", proxy: "", proxyChoice: "", proxyName: "", prefix: null, priority: "", issuer: "", all: true, models: [] };

  const label = ed.label.trim();
  if (adding ? label !== "" : label !== orig.label) body.label = label;

  const note = ed.note.trim();
  if (adding ? note !== "" : note !== orig.note) body.note = note;

  const proxyError = applyProxyChoice(body, ed, orig, adding);
  if (proxyError) return { error: proxyError };

  if (ed.prefixDefault) {
    if (!adding && orig.prefix !== null && orig.prefix !== undefined) body.prefix = null;
  } else {
    const prefix = cleanPrefix(ed.prefix);
    if (adding || prefix !== orig.prefix) body.prefix = prefix;
  }

  const priorityText = ed.priority.trim();
  if (priorityText !== "" && !/^-?\d+$/.test(priorityText)) return { error: "form.badPriority" };
  if (adding ? priorityText !== "" : priorityText !== orig.priority) body.priority = priorityText === "" ? null : Number(priorityText);

  // The saved token is never sent to the page: empty keeps it, the checkbox
  // removes it.
  const issuer = ed.issuerClear ? "" : ed.issuer.trim();
  if (issuer) {
    if (!/^wkt_\S+$/.test(issuer)) return { error: "form.badIssuer" };
    body.issuer_token = issuer;
  } else if (!adding && ed.issuerClear) {
    body.issuer_token = "";
  }

  if (ed.all) {
    if (!adding && !orig.all) body.models = null;
  } else {
    if (ed.selected.length === 0) return { error: "form.needModels" };
    const selected = [...ed.selected].sort();
    const before = orig.models.slice().sort();
    if (adding || orig.all || selected.join("\n") !== before.join("\n")) body.models = selected;
  }
  return { body };
}

/**
 * Puts the proxy choice into the body when it changed: proxy_url "" to
 * inherit, "direct", proxy_id for a list proxy, or the typed address.
 * Returns a message key when a custom address is missing.
 */
function applyProxyChoice(body: CredentialSaveRequest, ed: EditorState, orig: EditorOriginal, adding: boolean): string | null {
  const choice = ed.proxyChoice;
  if (choice === "custom") {
    // The input shows a saved address with its password redacted; only send
    // it when the user changed it.
    const proxy = ed.proxy.trim();
    if (!proxy) return "form.needProxy";
    if (adding || orig.proxyChoice !== "custom" || proxy !== orig.proxy) body.proxy_url = proxy;
    return null;
  }
  if (!adding && choice === orig.proxyChoice) return null;
  if (choice.startsWith("id:")) body.proxy_id = choice.slice(3);
  else if (choice === "direct") body.proxy_url = "direct";
  else if (!adding) body.proxy_url = "";
  return null;
}
