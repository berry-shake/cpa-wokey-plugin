import { messages } from "@core/i18n";
import en from "./i18n/en.json";
import zh from "./i18n/zh.json";

describe("messages", () => {
  const names = (text: string) => (text.match(/\{\w+\}/g) || []).sort();

  it("has the same keys in both languages", () => {
    expect(Object.keys(zh).sort()).toEqual(Object.keys(en).sort());
    expect(Object.keys(messages.zh).sort()).toEqual(Object.keys(messages.en).sort());
  });

  it("uses the same placeholders in both languages", () => {
    for (const key of Object.keys(messages.en)) expect([key, names(messages.zh[key])]).toEqual([key, names(messages.en[key])]);
  });
});
