import { describe, expect, it } from "vitest";
import { buildEpisodeCardLabels } from "./episodeCardLabels";

const game = {
  type: "episode",
  title: "Chicago Bulls vs Washington Wizards",
  series_title: "NBA",
  season_number: 2026,
  episode_number: 296,
};

describe("buildEpisodeCardLabels", () => {
  it("shows the season and episode code of a numbered series", () => {
    expect(buildEpisodeCardLabels({ ...game, air_date: "2026-11-20" })?.episodeCode).toBe(
      "S2026E296",
    );
  });

  // Sportarr numbers a league's games S2026E296; the day of the game is what
  // tells a viewer which one it is.
  it("shows the air date in place of the code when the series is labeled by date", () => {
    expect(
      buildEpisodeCardLabels({ ...game, episode_labels: "air_date", air_date: "2026-11-20" })
        ?.episodeCode,
    ).toBe("Nov 20, 2026");
  });

  it("keeps the code when a date-labeled episode has no air date", () => {
    expect(buildEpisodeCardLabels({ ...game, episode_labels: "air_date" })?.episodeCode).toBe(
      "S2026E296",
    );
  });
});
