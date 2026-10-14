// @vitest-environment node

import { describe, expect, it } from "vitest";

import { formatActiveScanTrigger } from "./scanRuns";

describe("formatActiveScanTrigger", () => {
  it.each([
    ["realtime_monitor", "File change"],
    ["autoscan", "Autoscan"],
    ["task:scan_libraries", "Scheduled"],
    ["library_created", "Created"],
    ["some_new_trigger", "some new trigger"],
  ])("labels %s as %s", (trigger, label) => {
    expect(formatActiveScanTrigger(trigger)).toBe(label);
  });
});
