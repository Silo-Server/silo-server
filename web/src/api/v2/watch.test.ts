import { describe, expect, it } from "vitest";

import getWatchStateOk from "../../../../contracts/api/v2/fixtures/get_watch_state_ok.json";

import { v2Fixture } from "./testing";
import { watchDetailFromV2 } from "./watch";

describe("watchDetailFromV2", () => {
  it("adapts the getWatchState fixture back to the player's WatchDetail", () => {
    const detail = watchDetailFromV2(v2Fixture<"GET /api/v2/watch/{id}">(getWatchStateOk));

    expect(detail.content_id).toBe("movie:heat-1995");
    expect(detail.overview).toBe("");

    const version = detail.versions[0];
    expect(version?.file_id).toBe(42);
    expect(version?.duration).toBe(10200);
    expect(version?.intro).toEqual({ start: 0, end: 90 });
    expect(version?.chapters?.[0]?.title).toBe("Opening");

    expect(detail.playback_variants?.[0]?.default_file_id).toBe(42);
    expect(detail.playback_variants?.[0]?.parts[0]?.default_file_id).toBe(42);

    expect(detail.subtitles[0]).toEqual({
      source: "embedded",
      language: "eng",
      codec: "",
      forced: false,
      hearing_impaired: false,
      title: "",
    });
    expect(detail.intro).toBeNull();
    expect(detail.credits).toEqual({ start: 10000, end: 10200 });
    expect(detail.user_data?.position_seconds).toBe(1325.5);
    expect(detail.user_data?.last_file_id).toBe(3);
    expect(detail.effective_subtitle_language).toBe("eng");
  });
});
