import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

// Embla measures layout jsdom does not have; the header is what is under test.
vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaRef: () => {},
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: () => {},
    scrollNext: () => {},
  }),
}));

import MediaCarousel from "./MediaCarousel";

describe("MediaCarousel", () => {
  it("links Explore all to the row's page", () => {
    render(
      <MemoryRouter>
        <MediaCarousel title="Trending Movies" viewAllHref="/requests/discover/trending_movies">
          <div>Heat</div>
        </MediaCarousel>
      </MemoryRouter>,
    );

    const link = screen.getByRole("link", { name: "Explore all Trending Movies" });
    expect(link).toHaveAttribute("href", "/requests/discover/trending_movies");
    expect(link).toHaveTextContent("Explore all");
  });

  it("keeps the Explore all button for rows that navigate themselves", () => {
    const onViewAll = vi.fn();
    render(
      <MemoryRouter>
        <MediaCarousel title="Recently Added" onViewAll={onViewAll}>
          <div>Heat</div>
        </MediaCarousel>
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Explore all" }));

    expect(onViewAll).toHaveBeenCalledOnce();
    expect(screen.queryByRole("link", { name: /Explore all/ })).not.toBeInTheDocument();
  });
});
