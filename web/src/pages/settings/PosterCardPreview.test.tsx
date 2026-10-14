import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { buildDefaultPrefs } from "@/lib/overlays";

import { PosterCardPreviewView } from "./PosterCardPreview";

vi.mock("@/components/overlays/CardOverlays", () => ({
  default: () => <div data-testid="badges" />,
}));

const VARIANTS = ["movie", "show", "requested"] as const;

function markup(props: Partial<Parameters<typeof PosterCardPreviewView>[0]> = {}) {
  return renderToStaticMarkup(
    <PosterCardPreviewView
      presentation={{ poster_size: "standard", caption: "title_metadata" }}
      overlayPrefs={buildDefaultPrefs()}
      quickActionMode="both"
      variants={VARIANTS}
      {...props}
    />,
  );
}

describe("PosterCardPreviewView", () => {
  // Every sample shows at every size, so the request sample and show-only
  // badges can be previewed at Standard, the default, too.
  it("shows every sample at every size, narrower for Compact", () => {
    for (const poster_size of ["compact", "standard", "large"] as const) {
      const html = markup({ presentation: { poster_size, caption: "title" } });
      expect(html.match(/aspect-\[2\/3\]/g)).toHaveLength(3);
      expect(html).toContain("Glass Harbor");
    }
    expect(markup({ presentation: { poster_size: "compact", caption: "title" } })).toContain(
      "w-[5.25rem]",
    );
  });

  it("follows the caption choice", () => {
    expect(markup()).toContain("2024 · 2h 28m");
    const titleOnly = markup({ presentation: { poster_size: "standard", caption: "title" } });
    expect(titleOnly).toContain("Northern Lights");
    expect(titleOnly).not.toContain("2024 · 2h 28m");
    expect(markup({ presentation: { poster_size: "standard", caption: "artwork" } })).not.toContain(
      "Northern Lights",
    );
  });

  it("draws badges only while they are on", () => {
    expect(markup()).toContain('data-testid="badges"');
    expect(markup({ overlayPrefs: null })).not.toContain('data-testid="badges"');
  });

  it("draws the chosen shortcut buttons and none when off", () => {
    expect(markup()).toContain("lucide-heart");
    expect(markup()).toContain("lucide-eye");
    const watchedOnly = markup({ quickActionMode: "watched" });
    expect(watchedOnly).toContain("lucide-eye");
    expect(watchedOnly).not.toContain("lucide-heart");
    expect(markup({ quickActionMode: "none" })).not.toContain("lucide-eye");
  });
});
