import { describe, expect, it } from "vitest";

import { artworkWidthQuery, type ArtworkLadders, type DrawnWidths } from "./artworkWidths";

// The ladders getImageCapabilities lists (contracts/api/v2/fixtures).
const ladders: ArtworkLadders = {
  slot_width_params: {
    poster: "poster_width",
    backdrop: "backdrop_width",
    logo: "logo_width",
    still: "still_width",
    photo: "photo_width",
  },
  widths: {
    poster: { small: 300, medium: 500, large: 1000 },
    backdrop: { small: 300, medium: 1920, large: 2560 },
    logo: { small: 500, medium: 500, large: 1280 },
    still: { small: 300, medium: 500, large: 1280 },
    profile: { small: 300, medium: 500, large: 500 },
  },
};

describe("artworkWidthQuery", () => {
  it.each<[string, DrawnWidths, number, ReturnType<typeof artworkWidthQuery>]>([
    // 370 device px; a rung down to 5% narrower is accepted, so the server
    // answers w500 for a 185px card at 2x.
    ["scales by the pixel ratio", { poster: 185 }, 2, { poster_width: 352 }],
    ["treats a missing ratio as 1x", { poster: 185 }, 0, { poster_width: 176 }],
    // Browser zoom reports ratios no screen has.
    ["caps the ratio at 3x", { poster: 100 }, 5, { poster_width: 285 }],
    // 513 device px would otherwise take the still's 1280px rung.
    ["takes a rung just short of the drawn width", { still: 171 }, 3, { still_width: 488 }],
    // 2880 device px: the 2560px rung covers 89%, so the original is not sent.
    ["stands the top rung in for the original", { backdrop: 1440 }, 2, { backdrop_width: 2560 }],
    // 3840 device px: the top rung covers 67%, so the server answers the original.
    ["asks for the original past the top rung", { backdrop: 1920 }, 2, { backdrop_width: 3648 }],
    ["leaves out undrawn slots", { poster: 0, logo: 480 }, 1, { logo_width: 456 }],
  ])("%s", (_name, drawn, ratio, expected) => {
    expect(artworkWidthQuery(drawn, ladders, ratio)).toEqual(expected);
  });

  it("sends nothing until the server lists slot widths", () => {
    expect(artworkWidthQuery({ poster: 185, backdrop: 1440 }, null, 2)).toEqual({});
    expect(
      artworkWidthQuery(
        { poster: 185, photo: 110 },
        { ...ladders, slot_width_params: { poster: "poster_width" } },
        2,
      ),
    ).toEqual({ poster_width: 352 });
  });
});
