import type { CredentialView, ModelView } from "../api/types";
import { makeT } from "../i18n";
import {
  addSelected,
  buildSaveBody,
  currentPrefix,
  modelMeta,
  newEditor,
  pickerRows,
  previewSample,
  registerPreview,
  toggleSelected,
  type EditorState,
} from "./editor";

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

describe("newEditor", () => {
  it("starts an add form on all models and the default prefix", () => {
    const ed = adding();
    expect(ed).toMatchObject({ mode: "add", all: true, prefixDefault: true, prefix: "wokey", selected: [], original: null, issuerPlaceholder: "wkt_…", labelPlaceholder: "" });
  });

  it("fills an edit form from the credential", () => {
    const ed = editing();
    expect(ed).toMatchObject({
      mode: "edit",
      authIndex: "3",
      name: "wokey-aaaa.json",
      title: "Team A",
      label: "Team A",
      labelPlaceholder: "wokey-aaaa",
      proxy: "socks5://u:***@h:1080",
      prefixDefault: false,
      prefix: "team",
      priority: "5",
      issuer: "",
      issuerPlaceholder: "wkt_****abcd",
      all: false,
      selected: ["gpt-5", "glm-4.6"],
    });
    expect(ed.original).toMatchObject({ prefix: "team", priority: "5", issuer: "wkt_****abcd", all: false, models: ["gpt-5", "glm-4.6"] });
  });

  it("fills the note, empty when there is none", () => {
    expect(editing(cred({ note: "for team A" }))).toMatchObject({ note: "for team A", original: { note: "for team A" } });
    expect(editing()).toMatchObject({ note: "", original: { note: "" } });
    expect(adding().note).toBe("");
  });

  it("uses the default prefix for credentials without one", () => {
    expect(editing(cred({ prefix: null }))).toMatchObject({ prefixDefault: true, prefix: "wokey" });
    expect(editing(cred({ prefix: "" }))).toMatchObject({ prefixDefault: false, prefix: "" });
  });
});

describe("model selection", () => {
  it("keeps the pick order", () => {
    expect(toggleSelected(["a", "b"], "c", true)).toEqual(["a", "b", "c"]);
    expect(toggleSelected(["a", "b"], "a", true)).toEqual(["a", "b"]);
    expect(toggleSelected(["a", "b"], "a", false)).toEqual(["b"]);
    expect(addSelected(["b"], ["a", "b", "c"])).toEqual(["b", "a", "c"]);
  });

  const models: ModelView[] = [
    { id: "gpt-5", name: "GPT-5", owned_by: "openai", pricing: {} },
    { id: "claude-sonnet-4-5", owned_by: "anthropic", pricing: {} },
  ];

  it("lists selected models the catalog lost first", () => {
    const rows = pickerRows(models, ["zz-old", "gpt-5", "aa-old"], "");
    expect(rows.map((r) => [r.id, !!r.missing])).toEqual([
      ["aa-old", true],
      ["zz-old", true],
      ["gpt-5", false],
      ["claude-sonnet-4-5", false],
    ]);
  });

  it("filters rows by id, name or owner", () => {
    expect(pickerRows(models, ["old-gpt"], "GPT").map((r) => r.id)).toEqual(["old-gpt", "gpt-5"]);
    expect(pickerRows(models, [], "anthropic").map((r) => r.id)).toEqual(["claude-sonnet-4-5"]);
    expect(pickerRows(models, [], "none")).toEqual([]);
  });

  it("describes a model's context and price", () => {
    expect(modelMeta(t, { context_length: 200000, pricing: { input: 3, output: 15 } })).toBe(
      t("picker.ctx", { ctx: "200K" }) + " · " + t("picker.price", { input: "3", output: "15" }),
    );
    expect(modelMeta(t, { pricing: {} })).toBe("");
  });
});

describe("register preview", () => {
  it("uses the first picked model, else the first in the catalog", () => {
    expect(previewSample(["b", "a"], [{ id: "x" }])).toBe("b");
    expect(previewSample([], [{ id: "x" }])).toBe("x");
    expect(previewSample([], [])).toBe("model-id");
  });

  it("mentions the bare name unless CPA forces the prefix", () => {
    expect(registerPreview(t, "wokey", "gpt-5", false)).toBe(t("form.registerAsBoth", { example: "wokey/gpt-5", bare: "gpt-5" }));
    expect(registerPreview(t, "wokey", "gpt-5", true)).toBe(t("form.registerAs", { example: "wokey/gpt-5" }));
    expect(registerPreview(t, "", "gpt-5", false)).toBe(t("form.registerAs", { example: "gpt-5" }));
  });

  it("takes the prefix from the default or the input", () => {
    expect(currentPrefix({ prefixDefault: true, prefix: "x" }, "/wokey/")).toBe("wokey");
    expect(currentPrefix({ prefixDefault: false, prefix: " /team/ " }, "wokey")).toBe("team");
  });
});

describe("buildSaveBody when adding", () => {
  it("needs a key", () => {
    expect(buildSaveBody(adding({ key: "  " }))).toEqual({ error: "form.needKey" });
  });

  it("sends the key, the default prefix and all models", () => {
    expect(buildSaveBody(adding({ key: " sk-wk-new ", skipVerify: true }))).toEqual({ body: { api_key: "sk-wk-new", skip_verify: true } });
  });

  it("sends only the fields that were filled", () => {
    const ed = adding({ key: "sk-wk-new", label: " Mine ", proxy: "direct", prefixDefault: false, prefix: "/p/", priority: "-3", issuer: "wkt_example", all: false, selected: ["b", "a"] });
    expect(buildSaveBody(ed)).toEqual({
      body: { api_key: "sk-wk-new", label: "Mine", proxy_url: "direct", prefix: "p", priority: -3, issuer_token: "wkt_example", models: ["a", "b"] },
    });
  });

  it("sends a trimmed note only when filled", () => {
    expect(buildSaveBody(adding({ key: "k", note: "  for team A  " }))).toEqual({ body: { api_key: "k", note: "for team A" } });
    expect(buildSaveBody(adding({ key: "k", note: "   " }))).toEqual({ body: { api_key: "k" } });
  });

  it("sends an empty custom prefix", () => {
    expect(buildSaveBody(adding({ key: "k", prefixDefault: false, prefix: "" }))).toEqual({ body: { api_key: "k", prefix: "" } });
  });

  it("rejects bad input", () => {
    expect(buildSaveBody(adding({ key: "k", priority: "1.5" }))).toEqual({ error: "form.badPriority" });
    expect(buildSaveBody(adding({ key: "k", issuer: "token" }))).toEqual({ error: "form.badIssuer" });
    expect(buildSaveBody(adding({ key: "k", all: false, selected: [] }))).toEqual({ error: "form.needModels" });
  });
});

describe("buildSaveBody when editing", () => {
  it("sends only the identity when nothing changed", () => {
    expect(buildSaveBody(editing())).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json" } });
    expect(buildSaveBody(editing(cred(), { selected: ["glm-4.6", "gpt-5"] }))).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json" } });
  });

  it("sends changed fields", () => {
    const ed = editing(cred(), { label: "", proxy: "", prefixDefault: true, priority: "", issuerClear: true, all: true });
    expect(buildSaveBody(ed)).toEqual({
      body: { auth_index: "3", name: "wokey-aaaa.json", label: "", proxy_url: "", prefix: null, priority: null, issuer_token: "", models: null },
    });
  });

  it("sends a changed or cleared note, not an unchanged one", () => {
    const withNote = cred({ note: "old" });
    expect(buildSaveBody(editing(withNote, { note: " new " }))).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json", note: "new" } });
    expect(buildSaveBody(editing(withNote, { note: "" }))).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json", note: "" } });
    expect(buildSaveBody(editing(withNote, { note: "old " }))).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json" } });
  });

  it("ignores a typed token once removal is ticked", () => {
    const body = buildSaveBody(editing(cred(), { issuer: "wkt_example", issuerClear: true }));
    expect(body).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json", issuer_token: "" } });
  });

  it("sends a new model list sorted", () => {
    const body = buildSaveBody(editing(cred(), { selected: ["gpt-5", "claude-sonnet-4-5"] }));
    expect(body).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json", models: ["claude-sonnet-4-5", "gpt-5"] } });
  });

  it("does not reset a default prefix that stays default", () => {
    expect(buildSaveBody(editing(cred({ prefix: null, all_models: true, models: null })))).toEqual({ body: { auth_index: "3", name: "wokey-aaaa.json" } });
  });
});
