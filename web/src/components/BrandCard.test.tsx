import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { DiscoverBrandCard } from "@/api/types";
import BrandCard from "./BrandCard";

function renderCard(props: Parameters<typeof BrandCard>[0]) {
  render(
    <MemoryRouter>
      <BrandCard {...props} />
    </MemoryRouter>,
  );
  return screen.getByRole("link", { name: props.card.display_name });
}

const card = (overrides: Partial<DiscoverBrandCard> = {}): DiscoverBrandCard => ({
  slug: "a24",
  display_name: "A24",
  ...overrides,
});

describe("BrandCard", () => {
  it("links a studio or network tile to its browse page", () => {
    expect(renderCard({ kind: "studio", card: card() })).toHaveAttribute(
      "href",
      "/requests/browse/studio/a24",
    );
  });

  it("opens a genre on movies unless series are asked for and supported", () => {
    expect(
      renderCard({ kind: "genre", card: card({ slug: "sci fi", display_name: "Sci-Fi" }) }),
    ).toHaveAttribute("href", "/requests/browse/genre/sci%20fi?media_type=movie");
    expect(
      renderCard({
        kind: "genre",
        card: card({ slug: "comedy", display_name: "Comedy" }),
        defaultMediaTypeForGenre: "series",
      }),
    ).toHaveAttribute("href", "/requests/browse/genre/comedy?media_type=movie");
  });

  it("opens a genre on series when asked and supported", () => {
    expect(
      renderCard({
        kind: "genre",
        card: card({ slug: "drama", display_name: "Drama", series_supported: true }),
        defaultMediaTypeForGenre: "series",
      }),
    ).toHaveAttribute("href", "/requests/browse/genre/drama?media_type=series");
  });
});
