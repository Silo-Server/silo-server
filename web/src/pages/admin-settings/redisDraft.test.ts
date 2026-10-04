import { describe, expect, it } from "vitest";

import { keepSavedRedisUrl, stageRedisUrl } from "./redisDraft";

// A settings form reduced to what the Redis fields use: the saved snapshot and
// the staged edits over it.
function draftOver(saved: Record<string, string>) {
  const staged = new Map<string, string>();
  return {
    staged,
    getValue: (key: string) => staged.get(key) ?? saved[key] ?? "",
    getPersistedValue: (key: string) => saved[key] ?? "",
    setValue: (key: string, value: string) => void staged.set(key, value),
    resetValue: (key: string) => void staged.delete(key),
  };
}

describe("stageRedisUrl", () => {
  it("stages the number on screen with a new URL", () => {
    const draft = draftOver({ "redis.db": "3" });

    stageRedisUrl(draft, "redis://cache.example.invalid:6379");

    expect(Object.fromEntries(draft.staged)).toEqual({
      "redis.url": "redis://cache.example.invalid:6379",
      "redis.db": "3",
    });
  });

  it("leaves a number the admin changed as they set it", () => {
    for (const edited of ["5", ""]) {
      const draft = draftOver({ "redis.db": "3" });
      draft.setValue("redis.db", edited);

      stageRedisUrl(draft, "redis://cache.example.invalid:6379/7");

      expect(draft.staged.get("redis.db")).toBe(edited);
    }
  });

  it("stages no number while the field shows none", () => {
    const draft = draftOver({});

    stageRedisUrl(draft, "redis://cache.example.invalid:6379/7");

    expect(Object.fromEntries(draft.staged)).toEqual({
      "redis.url": "redis://cache.example.invalid:6379/7",
    });
  });

  it("clears the number when Redis is switched off", () => {
    for (const edited of [undefined, "5"]) {
      const draft = draftOver({ "redis.db": "3" });
      if (edited !== undefined) draft.setValue("redis.db", edited);

      stageRedisUrl(draft, "");

      expect(Object.fromEntries(draft.staged)).toEqual({ "redis.url": "", "redis.db": "" });
    }
  });

  it("keeps a typed number through an emptied URL when none is saved", () => {
    const draft = draftOver({});
    draft.setValue("redis.db", "4");

    stageRedisUrl(draft, "");

    expect(Object.fromEntries(draft.staged)).toEqual({ "redis.url": "", "redis.db": "4" });
  });
});

describe("keepSavedRedisUrl", () => {
  it("withdraws the number that was staged with the URL", () => {
    const draft = draftOver({ "redis.db": "3" });
    stageRedisUrl(draft, "redis://cache.example.invalid:6379");

    keepSavedRedisUrl(draft);

    expect(draft.staged.size).toBe(0);
  });

  it("keeps a number the admin changed", () => {
    for (const edited of ["5", ""]) {
      const draft = draftOver({ "redis.db": "3" });
      draft.setValue("redis.db", edited);
      stageRedisUrl(draft, "redis://cache.example.invalid:6379");

      keepSavedRedisUrl(draft);

      expect(Object.fromEntries(draft.staged)).toEqual({ "redis.db": edited });
    }
  });
});
