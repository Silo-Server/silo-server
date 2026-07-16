import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import EpisodeRow from "./EpisodeRow";

vi.mock("@/components/overlays/CardOverlays", () => ({
  default: () => null,
}));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: null }),
}));

describe("EpisodeRow", () => {
  it("renders progress from inline episode user_data", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <EpisodeRow
          episode={{
            content_id: "ep-001",
            season_number: 1,
            episode_number: 1,
            title: "Pilot",
            overview: "",
            air_date: "2024-01-01",
            runtime: 42,
            still_url: "",
            still_thumbhash: "",
            user_data: {
              played: false,
              is_in_progress: true,
              position_seconds: 600,
              duration_seconds: 2400,
            },
            files: [],
          }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("width:25%");
  });

  it("renders a watched indicator from inline episode user_data", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <EpisodeRow
          episode={{
            content_id: "ep-002",
            season_number: 1,
            episode_number: 2,
            title: "Cat's in the Bag...",
            overview: "",
            air_date: null,
            runtime: 48,
            still_url: "",
            still_thumbhash: "",
            user_data: {
              played: true,
            },
            files: [],
          }}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain("text-success");
  });
});
