import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { EpisodeListItem } from "@/api/types";
import EpisodeCardCaption from "./EpisodeCardCaption";

function episode(overrides: Partial<EpisodeListItem> = {}): EpisodeListItem {
  return {
    content_id: "episode-1",
    season_number: 1,
    episode_number: 2,
    title: "Chapter 2",
    overview: "Oliver rushes headlong into a romance.",
    air_date: "2026-02-18",
    runtime: 52,
    still_url: "",
    still_thumbhash: "",
    files: [],
    ...overrides,
  };
}

describe("EpisodeCardCaption", () => {
  it("leaves out a spoiler-hidden description and its translation label", () => {
    render(
      <EpisodeCardCaption
        episode={episode({ machine_translated_fields: ["overview"] })}
        hideOverview
      />,
    );
    expect(screen.queryByText("Oliver rushes headlong into a romance.")).not.toBeInTheDocument();
    expect(screen.queryByText("Translated by AI")).not.toBeInTheDocument();
    expect(screen.getByText("S01E02 · Chapter 2")).toBeInTheDocument();
  });

  it("marks a translated description even without a date or runtime", () => {
    render(
      <EpisodeCardCaption
        episode={episode({ air_date: null, runtime: 0, machine_translated_fields: ["overview"] })}
        hideOverview={false}
      />,
    );
    expect(screen.getByText("Translated by AI")).toBeInTheDocument();
  });
});
