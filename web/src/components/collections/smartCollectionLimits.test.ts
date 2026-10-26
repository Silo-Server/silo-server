import { describe, expect, it } from "vitest";

import { createEmptyQueryDefinition } from "@/api/types";

import { normalizeSmartCollectionLimit } from "./smartCollectionLimits";

describe("normalizeSmartCollectionLimit", () => {
  it.each([
    ["no limit", undefined, undefined],
    ["the server's no-limit sentinel", 10_000_000, undefined],
    ["an explicit limit above the sentinel", 20_000_000, 20_000_000],
    ["an explicit limit", 250, 250],
    ["a limit above the old 500 cap", 2000, 2000],
  ])("maps %s to the editor's limit", (_label, stored, expected) => {
    const query = normalizeSmartCollectionLimit({ ...createEmptyQueryDefinition(), limit: stored });

    expect(query.limit).toBe(expected);
  });
});
