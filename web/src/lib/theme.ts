// Follows the management panel's theme: it stores {state: {theme}} under
// "cli-proxy-theme" in the shared localStorage. "auto" follows the system.

export const THEME_KEY = "cli-proxy-theme";

/** "dark", "white", or null for any other (unknown) theme name. */
export function resolveTheme(stored: string | null, prefersDark: boolean): "dark" | "white" | null {
  let theme = "auto";
  try {
    const parsed = JSON.parse(stored || "null") as { state?: { theme?: unknown } } | null;
    if (parsed && parsed.state && typeof parsed.state.theme === "string") theme = parsed.state.theme;
  } catch {
    /* keep auto */
  }
  if (theme === "auto") theme = prefersDark ? "dark" : "white";
  return theme === "dark" || theme === "white" ? theme : null;
}

export function applyPanelTheme(): void {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(THEME_KEY);
  } catch {
    stored = null;
  }
  const dark = !!window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
  const theme = resolveTheme(stored, dark);
  if (theme) document.documentElement.setAttribute("data-theme", theme);
  else document.documentElement.removeAttribute("data-theme");
}

export function followPanelTheme(): void {
  applyPanelTheme();
  if (window.matchMedia) {
    window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", applyPanelTheme);
  }
}
