// @vitest-environment node

import { describe, expect, it } from "vitest";
import { resolveSeriesPrimaryAction } from "./itemDetailLayout";

describe("resolveSeriesPrimaryAction", () => {
  function rollup(watched: number, total: number, inProgress = 0) {
    return {
      played: watched === total,
      watched_count: watched,
      unplayed_count: total - watched,
      in_progress_count: inProgress,
    };
  }

  it("has nothing to play when the server found no available episode", () => {
    expect(resolveSeriesPrimaryAction({ user_data: rollup(0, 10) })).toEqual({
      label: "Browse Series",
    });
  });
});
