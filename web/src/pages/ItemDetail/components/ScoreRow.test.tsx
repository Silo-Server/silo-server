import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import ScoreRow from "./ScoreRow";

describe("ScoreRow", () => {
  it("renders nothing without a score", () => {
    const { container } = render(<ScoreRow />);

    expect(container).toBeEmptyDOMElement();
  });

  it("keeps the library's IMDb and Rotten Tomatoes scores unlabelled", () => {
    render(<ScoreRow ratingImdb={8.24} ratingRtCritic={91} ratingRtAudience={85} />);

    expect(screen.getByText("8.2")).toBeInTheDocument();
    expect(screen.getByText("91%")).toBeInTheDocument();
    expect(screen.getByText("85%")).toBeInTheDocument();
    expect(screen.queryByText(/TMDB/)).not.toBeInTheDocument();
  });

  it("attributes a TMDB score and counts its votes", () => {
    render(<ScoreRow ratingTmdb={7.94} tmdbVoteCount={24_100} />);

    expect(screen.getByText("7.9")).toBeInTheDocument();
    expect(screen.getByText(/TMDB/)).toHaveTextContent("TMDB · 24.1K votes");
  });

  it("leaves out a missing TMDB vote count", () => {
    render(<ScoreRow ratingTmdb={6.5} tmdbVoteCount={0} />);

    expect(screen.getByText(/TMDB/)).toHaveTextContent(/^TMDB$/);
  });
});
