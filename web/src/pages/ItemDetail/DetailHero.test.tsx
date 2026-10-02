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

  it("names the title with its logo while title art is on", () => {
    const { container } = render(<DetailHero title="Blade Runner" logoUrl="/logo.webp" />);

    expect(container.querySelector('img[src="/logo.webp"]')).not.toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Blade Runner" })).toHaveClass("sr-only");
  });

  it("names the title in text when the profile turned title art off", () => {
    titleArt.show = false;
    const { container } = render(<DetailHero title="Blade Runner" logoUrl="/logo.webp" />);

    expect(container.querySelector('img[src="/logo.webp"]')).toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Blade Runner" })).not.toHaveClass(
      "sr-only",
    );
  });

  it("holds a logo title without fetching art until the profile's choice loads", () => {
    titleArt.show = undefined;
    const { container } = render(<DetailHero title="Blade Runner" logoUrl="/logo.webp" />);

    expect(container.querySelector('img[src="/logo.webp"]')).toBeNull();
    expect(screen.getByTestId("detail-hero-title-pending")).toHaveClass("h-20", "lg:h-28");
    expect(screen.getByRole("heading", { level: 1, name: "Blade Runner" })).toHaveClass("sr-only");
  });

  it("puts the type and network on one eyebrow line", () => {
    const { container } = render(
      <DetailHero title="Game of Thrones" context="Series" studioLabel="HBO" />,
    );

    const eyebrows = container.querySelectorAll(".detail-hero-context");
    expect(eyebrows).toHaveLength(1);
    expect(eyebrows[0]).toHaveTextContent(/^SeriesHBO$/);
  });

  it("keeps a breadcrumb context on its own row", () => {
    const { container } = render(
      <DetailHero title="Pilot" context={<nav aria-label="Breadcrumb">Show / Season 1</nav>} />,
    );

    const context = container.querySelector(".detail-hero-context");
    expect(context?.tagName).toBe("DIV");
    expect(context).toContainElement(screen.getByRole("navigation", { name: "Breadcrumb" }));
  });
});

describe("DetailHero artwork revisions", () => {
  it("keeps the above-fold primary content on one bounded reveal surface", () => {
    const { container } = render(<DetailHero title="Blade Runner" />);

    expect(container.querySelector(".detail-hero-primary-content")).not.toBeNull();
  });

  it("reserves the logo box before the image decodes", () => {
    const { container } = render(
      <DetailHero title="Blade Runner" logoUrl="/blade-runner-logo.rev-a.webp" />,
    );

    const logo = container.querySelector<HTMLImageElement>(
      'img[src="/blade-runner-logo.rev-a.webp"]',
    );
    expect(logo).toHaveClass("h-20", "w-full", "lg:h-28");
    expect(logo).not.toHaveClass("max-h-20", "lg:max-h-28");
  });

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

  it("keeps the backdrop placeholder behind the image throughout its fade", () => {
    const { container } = render(
      <DetailHero
        title="Blade Runner"
        backdropUrl="/backdrop.rev-a.webp"
        backdropThumbhash="placeholder"
      />,
    );

    const artwork = container.querySelector<HTMLElement>(".hero-backdrop-artwork");
    const backdrop = container.querySelector<HTMLImageElement>('img[src="/backdrop.rev-a.webp"]');
    expect(artwork).toHaveStyle({
      backgroundImage: 'url("data:image/png;base64,placeholder")',
    });
    expect(backdrop).toHaveClass("opacity-0", "transition-opacity", "duration-300");

    fireEvent.load(backdrop!);

    expect(backdrop).toHaveClass("opacity-100", "transition-opacity", "duration-300");
    expect(artwork).toHaveStyle({
      backgroundImage: 'url("data:image/png;base64,placeholder")',
    });
  });
});
