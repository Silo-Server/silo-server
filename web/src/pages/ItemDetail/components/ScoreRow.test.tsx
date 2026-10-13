import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import ScoreRow from "./ScoreRow";

describe("ScoreRow", () => {
  it("marks a TMDB score with TMDB's logo and counts its votes", () => {
    render(
      <ScoreRow
        ratings={[{ source: "tmdb", name: "TMDB", score: 79.4, display: "7.9" }]}
        tmdbVoteCount={24_100}
      />,
    );

    expect(screen.getByAltText("TMDB")).toBeInTheDocument();
    expect(screen.getByText("7.9")).toBeInTheDocument();
    expect(screen.getByText("24.1K votes")).toBeInTheDocument();
  });
});
