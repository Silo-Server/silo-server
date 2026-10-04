// @vitest-environment node

import { describe, expect, it } from "vitest";
import { makePreparation } from "@/test/downloadPreparations";
import {
  formatRemaining,
  formatSpeed,
  preparationPercent,
  preparationRemainingSeconds,
} from "./adminDownloadPreparationPresentation";

describe("download preparation presentation", () => {
  it("derives percent and time left from the latest reading", () => {
    const prep = makePreparation();
    expect(preparationPercent(prep)).toBe(25);
    // 4500 s of media left at 2× realtime.
    expect(preparationRemainingSeconds(prep)).toBe(2250);
    expect(formatRemaining(2250)).toBe("about 38 min left");
    expect(formatRemaining(30)).toBe("under 1 min left");
    expect(formatRemaining(2 * 3600 + 5 * 60)).toBe("about 2 h 5 min left");
    expect(formatSpeed(3.44)).toBe("3.4×");
    expect(formatSpeed(41.2)).toBe("41×");

    const unknown = makePreparation({ progress: undefined });
    expect(preparationPercent(unknown)).toBeNull();
    expect(preparationRemainingSeconds(unknown)).toBeNull();
    expect(formatRemaining(null)).toBe("");
    const stalled = makePreparation({
      progress: { encoded_seconds: 10, duration_seconds: 6000, speed: 0, updated_at: "x" },
    });
    expect(preparationRemainingSeconds(stalled)).toBeNull();
    const over = makePreparation({
      progress: { encoded_seconds: 7000, duration_seconds: 6000, speed: 1, updated_at: "x" },
    });
    expect(preparationPercent(over)).toBe(100);
  });
});
