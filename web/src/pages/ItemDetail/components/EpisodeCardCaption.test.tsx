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
  it("shows the code and title, the date and runtime, and the description", () => {
    render(<EpisodeCardCaption episode={episode()} hideOverview={false} />);
    expect(screen.getByText("S01E02 · Chapter 2")).toBeInTheDocument();
    expect(screen.getByText(/52m$/)).toBeInTheDocument();
    expect(screen.getByText("Oliver rushes headlong into a romance.")).toHaveClass("line-clamp-3");
  });

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

  it("puts badges on the title line and a notice on the date line", () => {
    render(
      <EpisodeCardCaption
        episode={episode()}
        hideOverview={false}
        badges={<span>watched</span>}
        notice={<span>Damaged file</span>}
      />,
    );
    const badges = screen.getByText("watched").parentElement!;
    expect(badges).toHaveClass("shrink-0");
    expect(badges.parentElement).toBe(
      screen.getByText("S01E02 · Chapter 2").closest("p")!.parentElement,
    );
    expect(screen.getByText("Damaged file").closest("div")).toBe(
      screen.getByText(/52m$/).closest("div"),
    );
  });

  it("gives screen readers the season and episode in words", () => {
    render(<EpisodeCardCaption episode={episode()} hideOverview={false} />);
    expect(screen.getByText("S01E02 · Chapter 2")).toHaveAttribute("aria-hidden", "true");
    expect(screen.getByText("Season 1, Episode 2, Chapter 2")).toHaveClass("sr-only");
  });
});
