import en from "./en.json";
import zh from "./zh.json";

export type Lang = "en" | "zh";
export type Params = Record<string, string | number | undefined>;
export type T = (key: string, params?: Params | null) => string;

export const LANG_KEY = "cliproxy-wokey-lang";
export const messages: Record<Lang, Record<string, string>> = { en, zh };

export function isLang(value: unknown): value is Lang {
  return value === "en" || value === "zh";
}

/** zh or en from the browser's language list; en when neither appears. */
export function detectDefaultLang(languages: readonly string[] | undefined, language?: string): Lang {
  const list = languages && languages.length ? languages : [language || "en"];
  for (const raw of list) {
    const tag = (raw || "").toLowerCase();
    if (tag.startsWith("zh")) return "zh";
    if (tag.startsWith("en")) return "en";
  }
  return "en";
}

export function initialLang(): Lang {
  try {
    const stored = localStorage.getItem(LANG_KEY);
    if (isLang(stored)) return stored;
  } catch {
    /* ignore */
  }
  return detectDefaultLang(navigator.languages, navigator.language);
}

export function saveLang(lang: Lang): void {
  try {
    localStorage.setItem(LANG_KEY, lang);
  } catch {
    /* ignore */
  }
}

/**
 * Looks a message up in the language, then in English, then falls back to
 * the key. {name} placeholders are replaced only when the param is defined.
 */
export function translate(lang: Lang, key: string, params?: Params | null): string {
  let template = messages[lang][key];
  if (template === undefined) template = messages.en[key];
  if (template === undefined) return key;
  if (params) {
    return template.replace(/\{(\w+)\}/g, (m, name: string) => (params[name] !== undefined ? String(params[name]) : m));
  }
  return template;
}

export function makeT(lang: Lang): T {
  return (key, params) => translate(lang, key, params);
}

/** BCP 47 tag for dates and times. */
export function localeTag(lang: Lang): string {
  return lang === "zh" ? "zh-CN" : "en-US";
}

/** The <html lang> value. */
export function htmlLang(lang: Lang): string {
  return lang === "zh" ? "zh-CN" : "en";
}
