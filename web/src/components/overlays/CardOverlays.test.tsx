import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";

import CardOverlays from "./CardOverlays";
import {
  OVERLAY_REGISTRY,
  SAMPLE_MOVIE_DATA,
  SAMPLE_SHOW_DATA,
  buildDefaultPrefs,
  type CardOverlayPrefs,
  type OverlayId,
  type PresetId,
} from "@/lib/overlays";

function prefsWithOnly(id: OverlayId, preset: PresetId = "classic"): CardOverlayPrefs {
  const prefs = buildDefaultPrefs();
  prefs.preset = preset;
  for (const key of Object.keys(prefs.items) as OverlayId[]) {
    prefs.items[key] = { ...prefs.items[key], enabled: key === id };
  }
  return prefs;
}

function badgeTexts(container: HTMLElement): (string | null)[] {
  return Array.from(container.querySelectorAll("span.inline-flex")).map((n) => n.textContent);
}

describe("CardOverlays", () => {
  it("renders a badge for every registered overlay given sample data", () => {
    for (const def of OVERLAY_REGISTRY) {
      const data =
        def.id === "network" || def.id === "show_status" ? SAMPLE_SHOW_DATA : SAMPLE_MOVIE_DATA;
      const expected = def.getValue(data);
      expect(expected, `sample data should exercise overlay ${def.id}`).toBeTruthy();
      const { container, unmount } = render(
        <CardOverlays data={data} prefs={prefsWithOnly(def.id)} />,
      );
      expect(badgeTexts(container), `overlay ${def.id}`).toEqual([expected]);
      unmount();
    }
  });

  it("shows 4K for a 2160p file on the standalone resolution badge", () => {
    const { container } = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("resolution")} />,
    );
    expect(badgeTexts(container)).toEqual(["4K"]);
  });

  it("suppresses standalone resolution and hdr when the combined badge is enabled", () => {
    const prefs = buildDefaultPrefs();
    prefs.items.resolution_hdr = { ...prefs.items.resolution_hdr, enabled: true };
    const texts = badgeTexts(
      render(<CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />).container,
    );
    expect(texts).toContain("4K DV");
    expect(texts).not.toContain("4K");
    expect(texts).not.toContain("DV HDR10");
  });

  it("honors prefs.order within a corner", () => {
    const prefs = buildDefaultPrefs(); // resolution, hdr, audio all top-left
    prefs.order = ["audio", "hdr", "resolution"];
    const { container } = render(<CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />);
    const topLeftStack = container.querySelector("div.top-2 > div.items-start");
    const texts = Array.from(topLeftStack?.querySelectorAll("span.inline-flex") ?? []).map(
      (n) => n.textContent,
    );
    expect(texts).toEqual(["Atmos", "DV HDR10", "4K"]);
  });

  it("suppresses the text label when a wordmark icon already spells it", () => {
    const data = { ...SAMPLE_MOVIE_DATA, hdr: "HDR10", audio: "Atmos", video_codec: "AV1" };
    for (const id of ["hdr", "audio", "video_codec"] as OverlayId[]) {
      const { container, unmount } = render(
        // pill prefers icons, so wordmarks resolve
        <CardOverlays data={data} prefs={prefsWithOnly(id, "pill")} />,
      );
      const badge = container.querySelector("span.inline-flex");
      expect(badge?.querySelector("svg"), `${id} should render its wordmark`).toBeTruthy();
      expect(badge?.querySelector("span.truncate"), `${id} label should be suppressed`).toBeNull();
      unmount();
    }
  });

  it("keeps the label when the icon does not spell it (DV HDR10)", () => {
    const { container } = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("hdr", "pill")} />,
    );
    const badge = container.querySelector("span.inline-flex");
    expect(badge?.textContent).toBe("DV HDR10");
    expect(badge?.querySelector("svg")).toBeTruthy();
  });

  it("renders HLG as a plain text label without an HDR wordmark", () => {
    const { container } = render(
      <CardOverlays
        data={{ ...SAMPLE_MOVIE_DATA, hdr: "HLG" }}
        prefs={prefsWithOnly("hdr", "pill")}
      />,
    );
    const badge = container.querySelector("span.inline-flex");
    expect(badge?.textContent).toBe("HLG");
    expect(badge?.querySelector("svg")).toBeNull();
  });

  it("caps each corner at three badges", () => {
    const prefs = buildDefaultPrefs();
    for (const key of Object.keys(prefs.items) as OverlayId[]) {
      prefs.items[key] = { ...prefs.items[key], enabled: true, position: "top-left" };
    }
    const { container } = render(<CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />);
    expect(container.querySelectorAll("span.inline-flex").length).toBe(3);
  });

  it("lifts bottom-right badges above the card menu button", () => {
    const prefs = prefsWithOnly("content_rating");
    prefs.items.content_rating = { ...prefs.items.content_rating, position: "bottom-right" };
    const poster = render(<CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />).container;
    expect(poster.querySelector("div.bottom-2 > div.items-end.mb-10")).toBeTruthy();
    const wide = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} variant="wide" />,
    ).container;
    expect(wide.querySelector("div.bottom-2 > div.items-end.mb-12")).toBeTruthy();
  });

  it("renders nothing when no enabled overlay has data", () => {
    const { container } = render(<CardOverlays data={{}} prefs={buildDefaultPrefs()} />);
    expect(container.querySelectorAll("span.inline-flex").length).toBe(0);
  });
});
