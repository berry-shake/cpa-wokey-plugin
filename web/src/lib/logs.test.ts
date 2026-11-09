import type { CredentialView } from "../api/types";
import {
  cacheRate,
  customRangeError,
  isFiltered,
  isLogRange,
  jumpTarget,
  lastPageOffset,
  latTone,
  localMinute,
  logAuthIndex,
  logCred,
  logCredential,
  logKey,
  logRangeParams,
  logRequestID,
  nextMinute,
  offsetForSize,
  pagerNumbers,
  pagerView,
  parseLogSize,
  restoreLogRange,
} from "./logs";

// Local wall-clock times, so the tests pass in any time zone.
const now = new Date(2026, 10, 8, 10, 30, 45, 500).getTime();
const DAY = 86400000;

describe("time ranges", () => {
  it("formats datetime-local values to the minute", () => {
    expect(localMinute(new Date(2026, 10, 8, 10, 30, 45))).toBe("2026-11-08T10:30");
    expect(localMinute(new Date(2026, 0, 2, 3, 4))).toBe("2026-01-02T03:04");
  });

  it("rounds up to the next minute", () => {
    expect(nextMinute(now)).toEqual(new Date(2026, 10, 8, 10, 31));
    expect(nextMinute(new Date(2026, 10, 8, 10, 31).getTime())).toEqual(new Date(2026, 10, 8, 10, 32));
  });

  it("validates custom bounds", () => {
    expect(customRangeError("2026-11-08T10:00", "2026-11-08T11:00")).toBe("");
    expect(customRangeError("2026-11-08T10:00:30", "2026-11-08T11:00:00.5")).toBe("");
    expect(customRangeError("", "2026-11-08T11:00")).toBe("logs.range.incomplete");
    expect(customRangeError("2026-11-08", "2026-11-08T11:00")).toBe("logs.range.incomplete");
    expect(customRangeError("2026-11-08T11:00", "2026-11-08T11:00")).toBe("logs.range.order");
    expect(customRangeError("2026-11-08T12:00", "2026-11-08T11:00")).toBe("logs.range.order");
  });

  it("sends nothing for all", () => {
    expect(logRangeParams({ range: "all", start: "", end: "" }, now)).toEqual({});
  });

  it("starts today at local midnight", () => {
    expect(logRangeParams({ range: "today", start: "", end: "" }, now)).toEqual({ from: new Date(2026, 10, 8).toISOString() });
  });

  it("counts relative ranges back from the next minute", () => {
    const end = new Date(2026, 10, 8, 10, 31).getTime();
    expect(logRangeParams({ range: "24h", start: "", end: "" }, now)).toEqual({ from: new Date(end - DAY).toISOString() });
    expect(logRangeParams({ range: "7d", start: "", end: "" }, now)).toEqual({ from: new Date(end - 7 * DAY).toISOString() });
    expect(logRangeParams({ range: "30d", start: "", end: "" }, now)).toEqual({ from: new Date(end - 30 * DAY).toISOString() });
  });

  it("sends both custom bounds, or nothing usable", () => {
    expect(logRangeParams({ range: "custom", start: "2026-11-01T08:00", end: "2026-11-02T09:15" }, now)).toEqual({
      from: new Date(2026, 10, 1, 8, 0).toISOString(),
      to: new Date(2026, 10, 2, 9, 15).toISOString(),
    });
    expect(logRangeParams({ range: "custom", start: "2026-11-02T09:15", end: "2026-11-01T08:00" }, now)).toBeNull();
  });

  it("restores the saved range and defaults custom bounds to the last day", () => {
    const fresh = restoreLogRange(null, now);
    expect(fresh).toEqual({ range: "all", start: "2026-11-07T10:31", end: "2026-11-08T10:31" });

    const saved = restoreLogRange(JSON.stringify({ range: "custom", start: "2026-11-01T08:00", end: "2026-11-02T09:15" }), now);
    expect(saved).toEqual({ range: "custom", start: "2026-11-01T08:00", end: "2026-11-02T09:15" });

    const badBounds = restoreLogRange(JSON.stringify({ range: "7d", start: "2026-11-02T09:15", end: "2026-11-01T08:00" }), now);
    expect(badBounds).toEqual({ ...fresh, range: "7d" });

    expect(restoreLogRange(JSON.stringify({ range: "year" }), now)).toEqual(fresh);
    expect(restoreLogRange("{not json", now)).toEqual(fresh);
  });

  it("knows the range names", () => {
    expect(isLogRange("24h")).toBe(true);
    expect(isLogRange("1h")).toBe(false);
    expect(isLogRange(undefined)).toBe(false);
  });

  it("calls a query filtered when any filter narrows it", () => {
    expect(isFiltered("all", "all", "  ")).toBe(false);
    expect(isFiltered("today", "all", "")).toBe(true);
    expect(isFiltered("all", "error", "")).toBe(true);
    expect(isFiltered("all", "all", "gpt")).toBe(true);
  });
});

describe("pagination", () => {
  it("accepts only the offered page sizes", () => {
    expect(parseLogSize("100")).toBe(100);
    expect(parseLogSize("30")).toBe(50);
    expect(parseLogSize(null)).toBe(50);
    expect(parseLogSize("abc")).toBe(50);
  });

  it("finds the page count and current page", () => {
    expect(pagerView(0, 0, 50)).toEqual({ pages: 1, current: 0 });
    expect(pagerView(50, 0, 50)).toEqual({ pages: 1, current: 0 });
    expect(pagerView(51, 50, 50)).toEqual({ pages: 2, current: 1 });
    expect(pagerView(1234, 600, 50)).toEqual({ pages: 25, current: 12 });
    // An offset past the end lands on the last page.
    expect(pagerView(120, 500, 50)).toEqual({ pages: 3, current: 2 });
  });

  it("shows the first, the last and two pages on each side", () => {
    expect(pagerNumbers(0, 1)).toEqual([0]);
    expect(pagerNumbers(0, 3)).toEqual([0, 1, 2]);
    expect(pagerNumbers(0, 25)).toEqual([0, 1, 2, 24]);
    expect(pagerNumbers(12, 25)).toEqual([0, 10, 11, 12, 13, 14, 24]);
    expect(pagerNumbers(24, 25)).toEqual([0, 22, 23, 24]);
  });

  it("keeps the first entry in view when the size changes", () => {
    expect(offsetForSize(150, 100)).toBe(100);
    expect(offsetForSize(150, 20)).toBe(140);
    expect(offsetForSize(0, 200)).toBe(0);
  });

  it("finds the last page's offset", () => {
    expect(lastPageOffset(120, 50)).toBe(100);
    expect(lastPageOffset(100, 50)).toBe(50);
    expect(lastPageOffset(1, 50)).toBe(0);
  });

  it("clamps jump targets", () => {
    expect(jumpTarget("3", 10)).toBe(3);
    expect(jumpTarget("0", 10)).toBe(1);
    expect(jumpTarget("-4", 10)).toBe(1);
    expect(jumpTarget("99", 10)).toBe(10);
    expect(jumpTarget("2.6", 10)).toBe(3);
    expect(jumpTarget("", 10)).toBeNull();
    expect(jumpTarget("abc", 10)).toBeNull();
  });
});

describe("latency tone", () => {
  it("rates time to first token", () => {
    expect(latTone(undefined, "ttft", 0)).toBe("");
    expect(latTone(0, "ttft", 0)).toBe("");
    expect(latTone(4999, "ttft", 0)).toBe("good");
    expect(latTone(5000, "ttft", 0)).toBe("warn");
    expect(latTone(10000, "ttft", 0)).toBe("bad");
  });

  it("rates long answers by output speed", () => {
    expect(latTone(10000, "total", 300)).toBe("good"); // 30 t/s
    expect(latTone(10000, "total", 150)).toBe("warn"); // 15 t/s
    expect(latTone(10000, "total", 149)).toBe("bad");
  });

  it("rates short answers by duration", () => {
    expect(latTone(9999, "total", 99)).toBe("good");
    expect(latTone(10000, "total", 0)).toBe("warn");
    expect(latTone(30000, "total", 0)).toBe("bad");
  });
});

describe("log entry display", () => {
  const creds = [
    { auth_index: "7", id: "wokey-aaaa.json", name: "wokey-aaaa.json", key_masked: "sk-wk-****aaaa" },
    { auth_index: "8", id: "id-b", name: "wokey-bbbb.json", key_masked: "sk-wk-****bbbb" },
  ] as CredentialView[];

  it("shows the request ID in full", () => {
    const id = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b";
    expect(logRequestID({ request_id: id })).toBe(id);
    expect(logRequestID({ request_id: "  " + id + " " })).toBe(id);
    expect(logRequestID({ request_id: "x".repeat(80) })).toHaveLength(80);
    expect(logRequestID({ request_id: "" })).toBe("—");
    expect(logRequestID({})).toBe("—");
  });

  it("finds the credential by auth file id or name", () => {
    expect(logCred({ auth_id: "wokey-aaaa.json" }, creds)?.auth_index).toBe("7");
    expect(logCred({ auth_id: "id-b" }, creds)?.auth_index).toBe("8");
    expect(logCred({ auth_id: "wokey-bbbb.json" }, creds)?.auth_index).toBe("8");
    expect(logCred({ auth_id: "gone.json" }, creds)).toBeNull();
    expect(logCred({ auth_id: "" }, creds)).toBeNull();
    expect(logAuthIndex({ auth_id: "id-b" }, creds)).toBe("8");
    expect(logAuthIndex({ auth_id: "gone.json" }, creds)).toBe("—");
  });

  it("shows the stored masked key, or the credential's for entries that stored the file name", () => {
    expect(logKey({ auth_id: "wokey-aaaa.json", credential: "sk-wk-****old1" }, creds)).toBe("sk-wk-****old1");
    expect(logKey({ auth_id: "wokey-aaaa.json", credential: "wokey-aaaa.json" }, creds)).toBe("sk-wk-****aaaa");
    expect(logKey({ auth_id: "gone.json", credential: "gone.json" }, creds)).toBe("");
    expect(logKey({ auth_id: "", credential: "" }, creds)).toBe("");
  });

  it("names the credential by its file stem", () => {
    expect(logCredential({ auth_id: "wokey-aaaa.json", credential: "sk-wk-****aaaa" }, creds)).toBe("wokey-aaaa");
    expect(logCredential({ auth_id: "", credential: "sk-wk-****cccc" }, creds)).toBe("sk-wk-****cccc");
    expect(logCredential({ auth_id: "", credential: "" }, creds)).toBe("—");
  });

  it("computes the cache hit share", () => {
    expect(cacheRate({ input_tokens: 48211, cached_tokens: 40960 })).toBeCloseTo(0.8496, 4);
    expect(cacheRate({ input_tokens: 0, cached_tokens: 0 })).toBeNull();
  });
});
