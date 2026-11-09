import { resolveTheme } from "./theme";

const stored = (theme: unknown) => JSON.stringify({ state: { theme } });

describe("resolveTheme", () => {
  it("follows the panel's explicit theme", () => {
    expect(resolveTheme(stored("dark"), false)).toBe("dark");
    expect(resolveTheme(stored("white"), true)).toBe("white");
  });

  it("follows the system for auto or no setting", () => {
    expect(resolveTheme(stored("auto"), true)).toBe("dark");
    expect(resolveTheme(stored("auto"), false)).toBe("white");
    expect(resolveTheme(null, true)).toBe("dark");
    expect(resolveTheme("{broken", false)).toBe("white");
    expect(resolveTheme(stored(42), true)).toBe("dark");
  });

  it("drops themes the page does not know", () => {
    expect(resolveTheme(stored("sepia"), false)).toBeNull();
  });
});
