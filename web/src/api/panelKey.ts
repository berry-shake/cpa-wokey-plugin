// Reuse the management key the CPA or CPAMP panel remembered. The panel and
// this page are same-origin, so they share localStorage "cli-proxy-auth": a
// zustand envelope XOR-obfuscated with a host-derived key. v2 payloads do not
// say which panel wrote them, so both derivations are tried; legacy v1 also
// mixes in the user agent. Read only when embedded, never written back, and
// only for this origin.

export const PANEL_AUTH_KEY = "cli-proxy-auth";
export const PANEL_SECRET_SALT = "cli-proxy-api-webui::secure-storage";

export function xorDecode(payload: string, keyText: string): string {
  const binary = atob(payload);
  const key = new TextEncoder().encode(keyText);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i) ^ key[i % key.length];
  return new TextDecoder().decode(bytes);
}

/** The inverse of xorDecode; used by tests to build panel payloads. */
export function xorEncode(text: string, keyText: string): string {
  const data = new TextEncoder().encode(text);
  const key = new TextEncoder().encode(keyText);
  let binary = "";
  for (let i = 0; i < data.length; i++) binary += String.fromCharCode(data[i] ^ key[i % key.length]);
  return btoa(binary);
}

export function panelAuthCandidates(raw: string, host: string, userAgent: string): string[] {
  try {
    if (raw.startsWith("enc::v2::")) {
      const body = raw.slice("enc::v2::".length);
      return [PANEL_SECRET_SALT + "|v2|" + host, PANEL_SECRET_SALT + "|" + host].map((key) => xorDecode(body, key));
    }
    if (raw.startsWith("enc::v1::")) {
      return [xorDecode(raw.slice("enc::v1::".length), PANEL_SECRET_SALT + "|" + host + "|" + userAgent)];
    }
  } catch {
    return [];
  }
  return [raw];
}

export function isThisOrigin(apiBase: string, origin: string): boolean {
  try {
    const trimmed = apiBase.trim();
    const base = /^https?:\/\//i.test(trimmed) ? trimmed : "http://" + trimmed;
    return new URL(base).origin === origin;
  } catch {
    return false;
  }
}

export interface PanelEnv {
  embedded: boolean;
  raw: string | null;
  host: string;
  origin: string;
  userAgent: string;
}

/** Extracts the remembered management key, or "" when there is none for this origin. */
export function panelKeyFrom(env: PanelEnv): string {
  if (!env.embedded || !env.raw) return "";
  for (const decoded of panelAuthCandidates(env.raw, env.host, env.userAgent)) {
    try {
      const parsed: unknown = JSON.parse(decoded);
      const envelope = parsed && typeof parsed === "object" ? (parsed as { state?: unknown }) : null;
      const saved = envelope ? ((envelope.state || envelope) as { managementKey?: unknown; apiBase?: unknown }) : null;
      if (saved && typeof saved.managementKey === "string" && saved.managementKey.trim() &&
          typeof saved.apiBase === "string" && isThisOrigin(saved.apiBase, env.origin)) {
        return saved.managementKey.trim();
      }
    } catch {
      /* wrong panel key for this payload; try the next one */
    }
  }
  return "";
}

export function readPanelKey(): string {
  let embedded = false;
  try {
    embedded = window.self !== window.top;
  } catch {
    embedded = false;
  }
  if (!embedded) return "";
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(PANEL_AUTH_KEY);
  } catch {
    return "";
  }
  return panelKeyFrom({
    embedded,
    raw,
    host: window.location.host,
    origin: window.location.origin,
    userAgent: navigator.userAgent,
  });
}
