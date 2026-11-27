import { describe, expect, it } from "vitest";

import { keepSavedRedisUrl, stageRedisUrl, type StagedRedisDb } from "./redisDraft";

/** A settings form's draft: staged values over a snapshot the test can move. */
function fakeDraft(persisted: Record<string, string>) {
  const staged = new Map<string, string>();
  return {
    persisted,
    staged,
    getValue: (key: string) => staged.get(key) ?? persisted[key] ?? "",
    getPersistedValue: (key: string) => persisted[key] ?? "",
    setValue: (key: string, value: string, { stage = false }: { stage?: boolean } = {}) => {
      if (!stage && value === (persisted[key] ?? "")) staged.delete(key);
      else staged.set(key, value);
    },
    resetValue: (key: string) => {
      staged.delete(key);
    },
  };
}

describe("keepSavedRedisUrl", () => {
  // The settings refresh while the replacement is typed and report another
  // number: taking the URL back must take back the number it staged (#4834).
  it("withdraws the number a URL edit staged after the snapshot moved", () => {
    const draft = fakeDraft({ "redis.url": "redis://saved:6379", "redis.db": "2" });
    const stagedDb: StagedRedisDb = { current: null };

    stageRedisUrl(draft, "redis://other:6379", stagedDb);
    expect(draft.staged.get("redis.db")).toBe("2");
    draft.persisted["redis.db"] = "3";
    keepSavedRedisUrl(draft, stagedDb);

    expect([...draft.staged.keys()]).toEqual([]);
  });

  it("keeps a number the admin typed before the URL", () => {
    const draft = fakeDraft({ "redis.url": "redis://saved:6379", "redis.db": "2" });
    const stagedDb: StagedRedisDb = { current: null };

    draft.setValue("redis.db", "5");
    stageRedisUrl(draft, "redis://o", stagedDb);
    stageRedisUrl(draft, "redis://other:6379", stagedDb);
    keepSavedRedisUrl(draft, stagedDb);

    expect([...draft.staged.entries()]).toEqual([["redis.db", "5"]]);
  });

  it("keeps claiming its number on each keystroke after the snapshot moved", () => {
    const draft = fakeDraft({ "redis.url": "redis://saved:6379", "redis.db": "2" });
    const stagedDb: StagedRedisDb = { current: null };

    stageRedisUrl(draft, "redis://o", stagedDb);
    draft.persisted["redis.db"] = "3";
    stageRedisUrl(draft, "redis://other:6379", stagedDb);
    keepSavedRedisUrl(draft, stagedDb);

    expect([...draft.staged.keys()]).toEqual([]);
  });

  it("keeps a number the admin typed after the URL", () => {
    const draft = fakeDraft({ "redis.url": "redis://saved:6379", "redis.db": "2" });
    const stagedDb: StagedRedisDb = { current: null };

    stageRedisUrl(draft, "redis://other:6379", stagedDb);
    draft.setValue("redis.db", "5");
    draft.persisted["redis.db"] = "3";
    keepSavedRedisUrl(draft, stagedDb);

    expect([...draft.staged.entries()]).toEqual([["redis.db", "5"]]);
  });
});
