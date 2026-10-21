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
    ["something-new", "lucide-film"],
    ["series", "lucide-tv"],
    ["audiobook", "lucide-headphones"],
    ["ebook", "lucide-book"],
  ])("marks %s artwork with %s", (mediaType, icon) => {
    expect(markClasses(mediaType)).toContain(icon);
  });
});
