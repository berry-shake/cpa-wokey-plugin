import type { CredentialView, ModelView } from "@core/api/types";
import { makeT } from "@core/i18n";
import { buildSaveBody, newEditor, pickerRows, type EditorState } from "@core/lib/editor";
import { filterModels } from "@core/lib/credentials";
import { plugin } from "./plugin";

const t = makeT("en");

function cred(over: Partial<CredentialView> = {}): CredentialView {
  return {
    auth_index: "3",
    id: "wokey-aaaa.json",
    name: "wokey-aaaa.json",
    label: "Team A",
    key_masked: "sk-wk-****aaaa",
    disabled: false,
    unavailable: false,
    priority: 5,
    prefix: "team",
    effective_prefix: "team",
    all_models: false,
    models: ["gpt-5", "glm-4.6"],
    registered_models: 2,
    registered_known: true,
    success: 0,
    failed: 0,
    proxy_url: "socks5://u:***@h:1080",
    issuer_token_masked: "wkt_****abcd",
    ...over,
  };
}

const adding = (over: Partial<EditorState> = {}): EditorState => ({ ...newEditor(1, "add", null, "wokey"), ...over });
const editing = (c: CredentialView = cred(), over: Partial<EditorState> = {}): EditorState => ({ ...newEditor(2, "edit", c, "wokey"), ...over });

describe("issuing token", () => {
  it("starts empty with the saved token as placeholder", () => {
    expect(adding()).toMatchObject({ issuer: "", issuerClear: false, issuerPlaceholder: "wkt_…", issuerSaved: "" });
    expect(editing()).toMatchObject({ issuer: "", issuerClear: false, issuerPlaceholder: "wkt_****abcd", issuerSaved: "wkt_****abcd" });
  });

  it("sends a new token, refuses a malformed one", () => {
    expect(buildSaveBody(adding({ key: "sk-wk-new", issuer: " wkt_example " }))).toEqual({ body: { api_key: "sk-wk-new", issuer_token: "wkt_example" } });
    expect(buildSaveBody(adding({ key: "k", issuer: "token" }))).toEqual({ error: "form.badIssuer" });
    // Checked after the priority, before the models.
    expect(buildSaveBody(adding({ key: "k", priority: "1.5", issuer: "token" }))).toEqual({ error: "form.badPriority" });
    expect(buildSaveBody(adding({ key: "k", issuer: "token", all: false, selected: [] }))).toEqual({ error: "form.badIssuer" });
  });

  it("removes the saved token and ignores a typed one once removal is ticked", () => {
    expect(buildSaveBody(editing(cred(), { issuerClear: true }))).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json", issuer_token: "" } });
    const body = buildSaveBody(editing(cred(), { issuer: "wkt_example", issuerClear: true }));
    expect(body).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json", issuer_token: "" } });
    expect(buildSaveBody(adding({ key: "k", issuerClear: true }))).toEqual({ body: { api_key: "k" } });
  });

  it("is blanked with the key when the dialog closes, and reloads the balance once saved", () => {
    expect(plugin.editor.clearSecrets!(editing(cred(), { issuer: "wkt_x" }))).toEqual({ issuer: "" });
    expect(plugin.editor.clearSecrets!(editing())).toBeNull();
    expect(plugin.editor.reloadsQuota!({ issuer_token: "" })).toBe(true);
    expect(plugin.editor.reloadsQuota!({ label: "x" })).toBe(false);
  });
});

describe("models", () => {
  const models: ModelView[] = [
    { id: "gpt-5", name: "GPT-5", owned_by: "openai", pricing: {} },
    { id: "claude-sonnet-4-5", owned_by: "anthropic", pricing: {} },
  ];

  it("filters by id, name or owner", () => {
    expect(pickerRows(models, ["old-gpt"], "GPT").map((r) => r.id)).toEqual(["old-gpt", "gpt-5"]);
    expect(pickerRows(models, [], "anthropic").map((r) => r.id)).toEqual(["claude-sonnet-4-5"]);
    expect(filterModels(models, " openai ").map((m) => m.id)).toEqual(["gpt-5"]);
  });

  it("describes a model's context and price", () => {
    expect(plugin.editor.modelMeta(t, { id: "m", context_length: 200000, pricing: { input: 3, output: 15 } })).toBe(
      t("picker.ctx", { ctx: "200K" }) + " · " + t("picker.price", { input: "3", output: "15" }),
    );
    expect(plugin.editor.modelMeta(t, { id: "m", pricing: {} })).toBe("");
    expect(plugin.editor.pickName(t, models[0])).toEqual({ text: "GPT-5", title: "GPT-5" });
    expect(plugin.editor.pickName(t, models[1])).toEqual({ text: "", title: "" });
  });
});
