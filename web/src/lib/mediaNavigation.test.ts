import { describe, expect, it } from "vitest";

import { buildMediaPlayHref } from "./mediaNavigation";

describe("buildMediaPlayHref", () => {
  it("routes podcasts to item detail instead of video watch", () => {
    expect(buildMediaPlayHref({ contentId: "podcast-1", type: "podcast" })).toBe(
      "/item/podcast-1",
    );
  });
});
