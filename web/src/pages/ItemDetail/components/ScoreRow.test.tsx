import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import ScoreRow from "./ScoreRow";

describe("ScoreRow", () => {
  it("renders nothing without a rating", () => {
    const { container } = render(<ScoreRow ratings={[]} />);

    expect(container).toBeEmptyDOMElement();
  });

  it("shows each rating as its source's mark and the server's display text", () => {
    render(
      <ScoreRow
        ratings={[
          { source: "imdb", name: "IMDb", score: 82, display: "8.2" },
          { source: "rt_critic", name: "RT", score: 91, display: "91%" },
          { source: "rt_audience", name: "RT Audience", score: 85, display: "85%" },
          { source: "letterboxd", name: "Letterboxd", score: 84, display: "4.2" },
        ]}
      />,
    );

    expect(screen.getByText("IMDb")).toBeInTheDocument();
    expect(screen.getByText("8.2")).toBeInTheDocument();
    expect(screen.getByText("RT")).toBeInTheDocument();
    expect(screen.getByText("91%")).toBeInTheDocument();
    expect(screen.getByText("RT Audience")).toBeInTheDocument();
    expect(screen.getByText("Letterboxd")).toBeInTheDocument();
    expect(screen.getByText("4.2")).toBeInTheDocument();
  });

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

  it("leaves out a missing TMDB vote count", () => {
    render(
      <ScoreRow
        ratings={[{ source: "tmdb", name: "TMDB", score: 65, display: "6.5" }]}
        tmdbVoteCount={0}
      />,
    );

    expect(screen.queryByText(/votes/)).not.toBeInTheDocument();
  });

  it("hides every rating after the third on phone widths", () => {
    render(
      <ScoreRow
        ratings={[
          { source: "imdb", name: "IMDb", score: 85, display: "8.5" },
          { source: "tmdb", name: "TMDB", score: 83, display: "8.3" },
          { source: "rt_critic", name: "RT", score: 93, display: "93%" },
          { source: "rt_audience", name: "RT Audience", score: 95, display: "95%" },
          { source: "metacritic", name: "Metacritic", score: 87, display: "87" },
        ]}
      />,
    );

    const entry = (text: string) =>
      screen.getByText(text).closest("span.inline-flex")?.parentElement;
    expect(entry("RT")).not.toHaveClass("max-sm:hidden");
    expect(entry("RT Audience")).toHaveClass("max-sm:hidden");
    expect(entry("Metacritic")).toHaveClass("max-sm:hidden");
  });
});
