import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import DetailHero from "./DetailHero";

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

  it("lets compact heroes grow when their content exceeds the viewport-based minimum", () => {
    const { container } = render(
      <DetailHero
        title="Ted Lasso: Season 3"
        variant="compact"
        overview={"A long season summary. ".repeat(40)}
      />,
    );

    const layout = container.querySelector(".page-shell-wide");
    expect(layout).toHaveClass("lg:min-h-[42vh]");
    expect(layout).not.toHaveClass("lg:h-[42vh]");
  });
  it("keeps the full-viewport backdrop static after it loads", () => {
    const { container } = render(
      <DetailHero title="Blade Runner" backdropUrl="/backdrop.rev-a.webp" />,
    );

    const backdrop = container.querySelector('img[src="/backdrop.rev-a.webp"]');
    const artwork = backdrop?.parentElement;
    expect(backdrop).not.toHaveClass("will-change-transform");
    expect(backdrop?.getAttribute("style") ?? "").not.toContain("animation");
    expect(artwork).toHaveClass("hero-backdrop-artwork");
    expect(artwork?.getAttribute("style") ?? "").not.toContain("filter");
  });

  it("places every detail-page action surface inside the opaque action boundary", () => {
    const { container } = render(
      <DetailHero title="Blade Runner" actions={<button>Play</button>} />,
    );

    expect(container.querySelector(".detail-action-bar")).toContainElement(
      screen.getByRole("button", { name: "Play" }),
    );
  });
});
