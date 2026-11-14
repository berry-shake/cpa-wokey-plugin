// Display formatters shared by every tab. Locale-dependent ones take a BCP 47
// tag; the plain number formatters use the browser's default locale like the
// old page did.

export function formatWhen(iso: string | undefined | null, locale: string): string {
  if (!iso) return "—";
  const parsed = new Date(iso);
  if (isNaN(parsed.valueOf())) return iso;
  return parsed.toLocaleString(locale);
}

export function formatTime(iso: string | undefined | null, locale: string): string {
  const parsed = new Date(iso as string);
  return isNaN(parsed.valueOf()) ? String(iso || "") : parsed.toLocaleTimeString(locale);
}

export function fmtUSD(value: unknown): string {
  const n = Number(value);
  if (!isFinite(n)) return "—";
  const abs = Math.abs(n);
  let text: string;
  if (abs === 0) text = "0";
  else if (abs >= 100) text = n.toFixed(0);
  else if (abs >= 0.01) text = n.toFixed(2);
  else text = n.toPrecision(2);
  return "$" + text;
}

/** USD per million tokens; "?" when unknown. */
export function fmtPrice(value: number | null | undefined): string {
  if (value === null || value === undefined) return "?";
  const n = Number(value);
  if (!isFinite(n)) return "?";
  if (n === 0) return "0";
  if (n >= 10) return String(Math.round(n * 100) / 100);
  return String(Number(n.toPrecision(3)));
}

export function fmtTokens(value: unknown): string {
  const n = Number(value) || 0;
  if (n >= 1000000) return Math.round(n / 100000) / 10 + "M";
  if (n >= 1000) return Math.round(n / 1000) + "K";
  return String(n);
}

export function fmtMS(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !(ms > 0)) return "—";
  if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
  const sec = Math.round(ms / 1000);
  return Math.floor(sec / 60) + "m " + (sec % 60) + "s";
}

export function fmtRate(v: number | null | undefined): string {
  return v === null || v === undefined ? "—" : Math.round(v).toLocaleString() + " t/s";
}

/** A speed without its unit, for lines that name the unit once. */
export function fmtRateNum(v: number | null | undefined): string {
  return v === null || v === undefined ? "—" : Math.round(v).toLocaleString();
}

export function fmtCount(n: unknown): string {
  return (Number(n) || 0).toLocaleString();
}

export function fmtPct(v: number | null | undefined): string {
  return v === null || v === undefined ? "—" : (v * 100).toFixed(1) + "%";
}

/** An auth file's name without extension: "wokey-ab12.json" -> "wokey-ab12". */
export function fileStem(name: string | undefined | null): string {
  const base = String(name || "").trim().split(/[\\/]/).pop() || "";
  const dot = base.lastIndexOf(".");
  return dot > 0 ? base.slice(0, dot) : base;
}

/** Plain YAML scalar unless it needs quoting; JSON strings are valid YAML. */
export function yamlScalar(value: unknown): string {
  const text = String(value);
  return text === "" || /^[\s'"{}[\],&*!|>%@`#?:-]|: | #|\s$/.test(text) ? JSON.stringify(text) : text;
}

/** Trims a model prefix and drops leading and trailing slashes. */
export function cleanPrefix(raw: string | null | undefined): string {
  return String(raw || "").trim().replace(/^\/+|\/+$/g, "");
}
