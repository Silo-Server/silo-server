import { describe, expect, it } from "vitest";

import { entryBoolean, readPluginEntryObject, updatePluginEntryObject } from "./pluginEntryValues";

/**
 * The layout pinned here is the one the server enforces: values are keyed by
 * SCHEMA ENTRY (`report`), each entry's value being a JSON string of that entry's
 * object. Writing a sub-field as its own key is rejected with "plugin user config
 * key \"api_key\" is not declared in schema".
 */
describe("readPluginEntryObject", () => {
  it("parses the entry's own JSON string", () => {
    const values = { report: '{"api_key":"abc","show_report":true}' };
    expect(readPluginEntryObject(values, "report")).toEqual({
      api_key: "abc",
      show_report: true,
    });
  });

  it("treats a missing, blank or unreadable entry as empty", () => {
    // Each of these has to render as "nothing configured" rather than throwing a
    // settings screen.
    expect(readPluginEntryObject(undefined, "report")).toEqual({});
    expect(readPluginEntryObject({}, "report")).toEqual({});
    expect(readPluginEntryObject({ report: "" }, "report")).toEqual({});
    expect(readPluginEntryObject({ report: "   " }, "report")).toEqual({});
    expect(readPluginEntryObject({ report: "not json" }, "report")).toEqual({});
    expect(readPluginEntryObject({ report: '["a"]' }, "report")).toEqual({});
    expect(readPluginEntryObject({ report: '"a"' }, "report")).toEqual({});
  });
});

describe("updatePluginEntryObject", () => {
  it("merges into the entry instead of replacing its other fields", () => {
    const values = { report: '{"api_key":"abc","show_report":true}' };
    const next = updatePluginEntryObject(values, "report", { show_report: false });
    expect(readPluginEntryObject(next, "report")).toEqual({
      api_key: "abc",
      show_report: false,
    });
  });

  it("keeps other entries, because the write replaces the whole set", () => {
    const values = { report: "{}", "other-entry": '{"x":"y"}' };
    const next = updatePluginEntryObject(values, "report", { show_report: true });
    expect(next["other-entry"]).toBe('{"x":"y"}');
  });

  it('stores a boolean as a boolean, not as the string "true"', () => {
    const next = updatePluginEntryObject({}, "report", { show_report: true });
    expect(next.report).toBe('{"show_report":true}');
    expect(entryBoolean(readPluginEntryObject(next, "report").show_report)).toBe(true);
  });

  it("starts an entry that was never set", () => {
    const next = updatePluginEntryObject(undefined, "report", { api_key: "abc" });
    expect(next).toEqual({ report: '{"api_key":"abc"}' });
  });
});

describe("entry accessors", () => {
  it("reads only a real boolean as true", () => {
    expect(entryBoolean(true)).toBe(true);
    // The string form is what an older client might have written; treating it as
    // true would enable a control the schema says is off.
    expect(entryBoolean("true")).toBe(false);
    expect(entryBoolean(undefined)).toBe(false);
    expect(entryBoolean(false)).toBe(false);
  });
});
