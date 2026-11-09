import { isThisOrigin, panelKeyFrom, PANEL_SECRET_SALT, xorDecode, xorEncode, type PanelEnv } from "./panelKey";

const host = "cpa.example:8317";
const origin = "http://cpa.example:8317";
const ua = "TestAgent/1.0";
const envelope = (apiBase: string, managementKey = "test-management-key") => JSON.stringify({ state: { apiBase, managementKey } });
const env = (raw: string | null, over: Partial<PanelEnv> = {}): PanelEnv => ({ embedded: true, raw, host, origin, userAgent: ua, ...over });

describe("panel key", () => {
  it("round-trips the XOR obfuscation", () => {
    expect(xorDecode(xorEncode("hello 世界", "k3y"), "k3y")).toBe("hello 世界");
  });

  it("reads v2 payloads written by either panel", () => {
    const cpa = "enc::v2::" + xorEncode(envelope(origin), PANEL_SECRET_SALT + "|v2|" + host);
    const cpamp = "enc::v2::" + xorEncode(envelope(origin), PANEL_SECRET_SALT + "|" + host);
    expect(panelKeyFrom(env(cpa))).toBe("test-management-key");
    expect(panelKeyFrom(env(cpamp))).toBe("test-management-key");
  });

  it("reads legacy v1 and plain payloads", () => {
    const v1 = "enc::v1::" + xorEncode(envelope(host), PANEL_SECRET_SALT + "|" + host + "|" + ua);
    expect(panelKeyFrom(env(v1))).toBe("test-management-key");
    expect(panelKeyFrom(env(envelope(origin + "/")))).toBe("test-management-key");
  });

  it("ignores keys for other origins, outside the panel, or without a key", () => {
    expect(panelKeyFrom(env(envelope("http://other:8317")))).toBe("");
    expect(panelKeyFrom(env(envelope(origin), { embedded: false }))).toBe("");
    expect(panelKeyFrom(env(envelope(origin, "  ")))).toBe("");
    expect(panelKeyFrom(env(null))).toBe("");
    expect(panelKeyFrom(env("enc::v2::!!not base64"))).toBe("");
  });

  it("compares origins with or without a scheme", () => {
    expect(isThisOrigin("cpa.example:8317", origin)).toBe(true);
    expect(isThisOrigin(" http://cpa.example:8317/management.html ", origin)).toBe(true);
    expect(isThisOrigin("https://cpa.example:8317", origin)).toBe(false);
    expect(isThisOrigin("::", origin)).toBe(false);
  });
});
