import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import DefaultArtwork from "./DefaultArtwork";

function markClasses(mediaType?: string | null) {
  const { container } = render(<DefaultArtwork mediaType={mediaType} />);
  const mark = container.querySelector(".default-artwork svg");
  return (mark?.getAttribute("class") ?? "").split(" ");
}

describe("DefaultArtwork", () => {
  it.each([
    ["movie", "lucide-film"],
    [undefined, "lucide-film"],
    [null, "lucide-film"],
    ["something-new", "lucide-film"],
    ["series", "lucide-tv"],
    ["season", "lucide-tv"],
    ["episode", "lucide-tv"],
    ["audiobook", "lucide-headphones"],
    ["book", "lucide-headphones"],
    ["books", "lucide-headphones"],
    ["podcast", "lucide-headphones"],
    ["podcasts", "lucide-headphones"],
    ["ebook", "lucide-book"],
    ["ebooks", "lucide-book"],
    ["manga", "lucide-book"],
    ["comic", "lucide-book"],
    ["comics", "lucide-book"],
  ])("marks %s artwork with %s", (mediaType, icon) => {
    expect(markClasses(mediaType)).toContain(icon);
  });

  it("is decoration only", () => {
    const { container } = render(<DefaultArtwork mediaType="series" />);
    const artwork = container.querySelector(".default-artwork");
    expect(artwork).toHaveAttribute("aria-hidden", "true");
    expect(artwork?.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(artwork).toHaveTextContent("");
  });
});
