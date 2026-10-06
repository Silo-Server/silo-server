import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import DetailHero from "./DetailHero";

const titleArt = vi.hoisted(() => ({ show: true as boolean | undefined }));

vi.mock("@/lib/thumbhash", () => ({
  decodeThumbhash: (thumbhash: string) => `data:image/png;base64,${thumbhash}`,
}));

vi.mock("@/hooks/useTitleArt", () => ({
  useShowTitleArt: () => titleArt.show,
}));

describe("DetailHero title", () => {
  beforeEach(() => {
    titleArt.show = true;
  });

  it("holds a logo title without fetching art until the profile's choice loads", () => {
    titleArt.show = undefined;
    const { container, rerender } = render(
      <DetailHero title="Blade Runner" logoUrl="/logo.webp" />,
    );

    expect(container.querySelector('img[src="/logo.webp"]')).toBeNull();
    expect(screen.getByTestId("detail-hero-title-pending")).toHaveClass("h-20", "lg:h-28");
    expect(screen.getByRole("heading", { level: 1, name: "Blade Runner" })).toHaveClass("sr-only");

    titleArt.show = true;
    rerender(<DetailHero title="Blade Runner" logoUrl="/logo.webp" />);
    expect(container.querySelector('img[src="/logo.webp"]')).not.toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Blade Runner" })).toHaveClass("sr-only");

    titleArt.show = false;
    rerender(<DetailHero title="Blade Runner" logoUrl="/logo.webp" />);
    expect(container.querySelector('img[src="/logo.webp"]')).toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Blade Runner" })).not.toHaveClass(
      "sr-only",
    );
  });
});

describe("DetailHero artwork revisions", () => {
  it("treats a changed poster URL as unloaded until that revision finishes loading", () => {
    const { rerender } = render(<DetailHero title="Blade Runner" posterUrl="/poster.rev-a.webp" />);

    const first = screen.getByRole("img", { name: "Blade Runner" });
    const firstPlaceholder = screen.getByTestId("detail-hero-poster-placeholder");
    expect(first).toHaveClass("opacity-0");
    expect(first).not.toHaveClass("transition-opacity");
    expect(firstPlaceholder).toHaveClass("opacity-100", "transition-opacity");
    fireEvent.load(first);
    expect(first).toHaveClass("opacity-100");
    expect(firstPlaceholder).toHaveClass("opacity-0");

    rerender(<DetailHero title="Blade Runner" posterUrl="/poster.rev-b.webp" />);

    const replacement = screen.getByRole("img", { name: "Blade Runner" });
    const replacementPlaceholder = screen.getByTestId("detail-hero-poster-placeholder");
    expect(replacement).toHaveAttribute("src", "/poster.rev-b.webp");
    expect(replacement).toHaveClass("opacity-0");
    expect(replacementPlaceholder).toHaveClass("opacity-100");
    fireEvent.load(replacement);
    expect(replacement).toHaveClass("opacity-100");
    expect(replacementPlaceholder).toHaveClass("opacity-0");
  });

  it("keeps loaded artwork on screen when only its signature changes", () => {
    const rev = "3f9a".repeat(16);
    const signed = (image: string, exp: number, sig: string) =>
      `/api/v2/artwork/tmdb/movies/78/${image}/w780.${rev}.webp?exp=${exp}&sig=${sig}`;
    const hero = (exp: number, sig: string) => (
      <DetailHero
        title="Blade Runner"
        posterUrl={signed("poster", exp, sig)}
        posterThumbhash="poster"
        backdropUrl={signed("backdrop", exp, sig)}
        backdropThumbhash="backdrop"
      />
    );
    const { container, rerender } = render(hero(1758621600, "aaaa"));
    const poster = screen.getByRole("img", { name: "Blade Runner" });
    const backdrop = container.querySelector<HTMLImageElement>(".hero-backdrop-artwork img")!;
    fireEvent.load(poster);
    fireEvent.load(backdrop);

    rerender(hero(1758622500, "bbbb"));

    const nextPoster = screen.getByRole("img", { name: "Blade Runner" });
    const nextBackdrop = container.querySelector<HTMLImageElement>(".hero-backdrop-artwork img")!;
    // The browser keeps painting an <img>'s current pixels while its new src
    // loads, so the element must survive and stay visible.
    const remounts = Number(nextPoster !== poster) + Number(nextBackdrop !== backdrop);
    const placeholderFlashes =
      Number(
        screen.getByTestId("detail-hero-poster-placeholder").classList.contains("opacity-100"),
      ) + Number(nextBackdrop.classList.contains("opacity-0"));
    expect({ remounts, placeholderFlashes }).toEqual({ remounts: 0, placeholderFlashes: 0 });
    expect(nextPoster).toHaveAttribute("src", signed("poster", 1758622500, "bbbb"));
    expect(nextBackdrop).toHaveAttribute("src", signed("backdrop", 1758622500, "bbbb"));
  });

  it("falls back to the placeholders when a re-signed request fails", () => {
    const rev = "3f9a".repeat(16);
    const signed = (image: string, exp: number, sig: string) =>
      `/api/v2/artwork/tmdb/movies/78/${image}/w780.${rev}.webp?exp=${exp}&sig=${sig}`;
    const hero = (exp: number, sig: string) => (
      <DetailHero
        title="Blade Runner"
        posterUrl={signed("poster", exp, sig)}
        posterThumbhash="poster"
        backdropUrl={signed("backdrop", exp, sig)}
        backdropThumbhash="backdrop"
      />
    );
    const { container, rerender } = render(hero(1758621600, "aaaa"));
    fireEvent.load(screen.getByRole("img", { name: "Blade Runner" }));
    fireEvent.load(container.querySelector<HTMLImageElement>(".hero-backdrop-artwork img")!);

    rerender(hero(1758622500, "bbbb"));
    const poster = screen.getByRole("img", { name: "Blade Runner" });
    const backdrop = container.querySelector<HTMLImageElement>(".hero-backdrop-artwork img")!;
    fireEvent.error(poster);
    fireEvent.error(backdrop);

    // A failed <img> shows the broken-image state, so each one must hide and
    // let its thumbhash show through again.
    const placeholdersShown =
      Number(
        poster.classList.contains("opacity-0") &&
          screen.getByTestId("detail-hero-poster-placeholder").classList.contains("opacity-100"),
      ) + Number(backdrop.classList.contains("opacity-0"));
    expect(placeholdersShown).toBe(2);
  });

  it("hides signed artwork again when its revision changes", () => {
    const signed = (image: string, rev: string) =>
      `/api/v2/artwork/tmdb/movies/78/${image}/w780.${rev.repeat(16)}.webp?exp=1758621600&sig=aaaa`;
    const hero = (rev: string) => (
      <DetailHero
        title="Blade Runner"
        posterUrl={signed("poster", rev)}
        backdropUrl={signed("backdrop", rev)}
      />
    );
    const { container, rerender } = render(hero("3f9a"));
    fireEvent.load(screen.getByRole("img", { name: "Blade Runner" }));
    fireEvent.load(container.querySelector<HTMLImageElement>(".hero-backdrop-artwork img")!);

    rerender(hero("8c21"));

    expect(screen.getByRole("img", { name: "Blade Runner" })).toHaveClass("opacity-0");
    expect(screen.getByTestId("detail-hero-poster-placeholder")).toHaveClass("opacity-100");
    expect(container.querySelector(".hero-backdrop-artwork img")).toHaveClass("opacity-0");
  });
});
