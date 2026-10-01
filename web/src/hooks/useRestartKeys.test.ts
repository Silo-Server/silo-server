import { describe, expect, it } from "vitest";

import { createRestartKeyMatcher } from "./useRestartKeys";

describe("createRestartKeyMatcher", () => {
  it("matches keys listed exactly", () => {
    const matcher = createRestartKeyMatcher({ keys: ["auth.jwt_secret"], prefixes: [] });

    expect(matcher.has("auth.jwt_secret")).toBe(true);
    expect(matcher.has("auth.jwt_expiry")).toBe(false);
  });

  it("matches every key under a listed prefix", () => {
    const matcher = createRestartKeyMatcher({ keys: [], prefixes: ["database.", "redis."] });

    expect(matcher.has("database.max_connections")).toBe(true);
    expect(matcher.has("redis.url")).toBe(true);
    // The prefix includes its trailing dot, so a sibling namespace that merely
    // starts with the same word must not be badged.
    expect(matcher.has("databases_extra.url")).toBe(false);
    expect(matcher.has("branding.server_name")).toBe(false);
  });

  it("treats a missing or malformed payload as 'nothing needs a restart'", () => {
    expect(createRestartKeyMatcher(undefined).has("database.max_connections")).toBe(false);
    expect(
      createRestartKeyMatcher({ keys: [], prefixes: [] }).has("database.max_connections"),
    ).toBe(false);
    // An older server can answer with nulls where the arrays should be.
    const malformed = { keys: null, prefixes: null } as unknown as {
      keys: string[];
      prefixes: string[];
    };
    expect(createRestartKeyMatcher(malformed).has("auth.jwt_secret")).toBe(false);
  });

  it("ignores empty strings so a blank prefix cannot match everything", () => {
    const matcher = createRestartKeyMatcher({ keys: ["", "s3.bucket"], prefixes: [""] });

    expect(matcher.has("s3.bucket")).toBe(true);
    expect(matcher.has("branding.server_name")).toBe(false);
  });
});
