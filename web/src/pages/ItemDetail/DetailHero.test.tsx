import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import DetailHero from "./DetailHero";

function stubHeroMedia({ reduce = false, wide = true } = {}) {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: query.includes("prefers-reduced-motion")
          ? reduce
          : query.includes("min-width")
            ? wide
            : false,
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: () => false,
        onchange: null,
      }) as unknown as MediaQueryList,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
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
});

describe("DetailHero trailer backdrop", () => {
  const trailer = {
    kind: "trailer",
    site: "youtube",
    site_key: "dQw4w9wgGcQ",
    is_official: true,
  };

  it("embeds a muted looping YouTube iframe when autoplay is allowed", async () => {
    stubHeroMedia();
    render(<DetailHero title="Blade Runner" backdropUrl="/backdrop.webp" heroTrailer={trailer} />);

    const iframe = (await screen.findByTestId("detail-hero-trailer")).querySelector("iframe");
    expect(iframe).not.toBeNull();
    expect(iframe).toHaveAttribute(
      "src",
      expect.stringContaining("https://www.youtube-nocookie.com/embed/dQw4w9wgGcQ?"),
    );
    expect(iframe?.getAttribute("src")).toContain("mute=1");
    expect(iframe?.getAttribute("src")).toContain("autoplay=1");
  });

  it("keeps the still backdrop when reduced motion is requested", () => {
    stubHeroMedia({ reduce: true });
    render(<DetailHero title="Blade Runner" backdropUrl="/backdrop.webp" heroTrailer={trailer} />);
    expect(screen.queryByTestId("detail-hero-trailer")).not.toBeInTheDocument();
  });

  it("keeps the still backdrop on a narrow viewport", () => {
    stubHeroMedia({ wide: false });
    render(<DetailHero title="Blade Runner" backdropUrl="/backdrop.webp" heroTrailer={trailer} />);
    expect(screen.queryByTestId("detail-hero-trailer")).not.toBeInTheDocument();
  });
});
