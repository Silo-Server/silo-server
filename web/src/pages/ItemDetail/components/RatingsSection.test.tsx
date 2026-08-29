import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import RatingsSection from "./RatingsSection";

const mocks = vi.hoisted(() => ({
  useCommunityRatings: vi.fn(),
  mutate: vi.fn(),
}));

vi.mock("@/hooks/queries/ratings", () => ({
  useCommunityRatings: mocks.useCommunityRatings,
  useSetCommunityRatingReaction: () => ({
    mutate: mocks.mutate,
    isPending: false,
  }),
}));

vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaRef: () => {},
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: vi.fn(),
    scrollNext: vi.fn(),
  }),
}));

describe("RatingsSection", () => {
  beforeEach(() => {
    mocks.mutate.mockReset();
    mocks.useCommunityRatings.mockReturnValue({
      data: {
        average_rating: 4.5,
        vote_count: 2,
        ratings: [
          {
            key: "rating-other",
            display_name: "S*******",
            avatar_url: "/profile-avatars/avatar-1.svg",
            rating: 5,
            rated_at: "2026-08-29T08:00:00Z",
            up_count: 3,
            down_count: 1,
            is_viewer: false,
          },
          {
            key: "rating-viewer",
            display_name: "B***",
            rating: 4,
            rated_at: "2026-07-22T08:00:00Z",
            up_count: 1,
            down_count: 0,
            viewer_reaction: "up",
            is_viewer: true,
          },
        ],
      },
    });
  });

  it("renders opaque-style cards with stars and separate reaction tallies", () => {
    render(<RatingsSection itemId="movie-1" />);

    expect(screen.getByRole("heading", { name: "Ratings" })).toBeInTheDocument();
    expect(screen.getByText("4.5 average from 2 ratings")).toBeInTheDocument();
    expect(screen.getByText("S*******")).toBeInTheDocument();
    expect(screen.getByText("You")).toBeInTheDocument();
    expect(screen.getByText("August 29, 2026")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "5 out of 5 stars" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Helpful: 3" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Not helpful: 1" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Helpful: 3" })).toHaveClass(
      "enabled:cursor-pointer",
    );
    expect(screen.getByRole("list")).not.toHaveClass("cursor-grab");
    expect(screen.getByLabelText("Ratings carousel")).toBeInTheDocument();
    expect(screen.getAllByRole("article")[0]).toHaveClass("household-rating-card");
  });

  it("toggles reactions on other and own cards, including removing a selected reaction", () => {
    render(<RatingsSection itemId="movie-1" />);

    fireEvent.click(screen.getByRole("button", { name: "Helpful: 3" }));
    expect(mocks.mutate).toHaveBeenCalledWith({ ratingKey: "rating-other", reaction: "up" });

    const ownHelpful = screen.getByRole("button", { name: "Helpful: 1" });
    expect(ownHelpful).toBeEnabled();
    fireEvent.click(ownHelpful);
    expect(mocks.mutate).toHaveBeenLastCalledWith({
      ratingKey: "rating-viewer",
      reaction: null,
    });
  });

  it("disables reaction controls when the server capability is unavailable", () => {
    const current = mocks.useCommunityRatings();
    mocks.useCommunityRatings.mockReturnValue({
      ...current,
      capabilities: {
        community_ratings: true,
        community_rating_reactions: false,
      },
    });

    render(<RatingsSection itemId="movie-1" />);

    expect(screen.getByRole("button", { name: "Helpful: 3" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Not helpful: 1" })).toBeDisabled();
  });

  it("keeps the section visible when no one has rated the item", () => {
    mocks.useCommunityRatings.mockReturnValue({
      data: { average_rating: null, vote_count: 0, ratings: [] },
      isLoading: false,
      isError: false,
    });

    render(<RatingsSection itemId="movie-1" />);

    expect(screen.getByRole("heading", { name: "Ratings" })).toBeInTheDocument();
    expect(screen.getByText("No ratings yet")).toBeInTheDocument();
    expect(screen.queryByRole("article")).not.toBeInTheDocument();
  });

  it("distinguishes the viewer when two private display names look identical", () => {
    mocks.useCommunityRatings.mockReturnValue({
      data: {
        average_rating: 5,
        vote_count: 2,
        ratings: [
          {
            key: "rating-viewer",
            display_name: "b***",
            avatar_url: "/viewer.webp",
            rating: 5,
            rated_at: "2026-08-29T08:00:00Z",
            up_count: 1,
            down_count: 0,
            is_viewer: true,
          },
          {
            key: "rating-other",
            display_name: "b***",
            rating: 5,
            rated_at: "2026-08-13T08:00:00Z",
            up_count: 1,
            down_count: 0,
            is_viewer: false,
          },
        ],
      },
    });

    render(<RatingsSection itemId="movie-1" />);

    expect(screen.getAllByText("b***")).toHaveLength(2);
    expect(screen.getByText("You")).toBeInTheDocument();
  });
});
