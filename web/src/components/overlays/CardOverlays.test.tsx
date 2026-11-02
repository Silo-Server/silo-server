import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ATTENTION_ACCENT,
  PRESET_IDS,
  SAMPLE_MOVIE_DATA,
  buildDefaultPrefs,
  type CardOverlayPrefs,
  type OverlayData,
  type OverlayId,
  type PresetId,
} from "@/lib/overlays";
import CardOverlays from "./CardOverlays";

function prefsWithOnly(id: OverlayId, preset: PresetId = "classic"): CardOverlayPrefs {
  const prefs = buildDefaultPrefs();
  prefs.preset = preset;
  for (const key of Object.keys(prefs.items) as OverlayId[]) {
    prefs.items[key] = { ...prefs.items[key], enabled: key === id };
  }
  return prefs;
}

/** Reports `width` for every observed poster; returns the observer's disconnect spy. */
function observePosterWidth(width: number) {
  const disconnect = vi.fn();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      constructor(private readonly callback: ResizeObserverCallback) {}
      observe(target: Element) {
        this.callback(
          [{ target, contentRect: { width } } as unknown as ResizeObserverEntry],
          this as unknown as ResizeObserver,
        );
      }
      disconnect = disconnect;
    },
  );
  return disconnect;
}

function badgeTexts(container: HTMLElement): (string | null)[] {
  return Array.from(container.querySelectorAll("span.inline-flex")).map((n) => n.textContent);
}

describe("CardOverlays", () => {
  beforeEach(() => vi.stubGlobal("CSS", { supports: () => true }));
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
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

  it("scales the active theme radius with a rounded poster preset", () => {
    vi.spyOn(window, "getComputedStyle").mockReturnValue({
      borderTopLeftRadius: "20px",
    } as CSSStyleDeclaration);
    const disconnect = observePosterWidth(92.5);

    const { container, unmount } = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("resolution", "minimal")} />,
    );
    const layer = container.querySelector<HTMLElement>('[data-card-overlays="poster"]');

    expect(layer?.style.getPropertyValue("--card-overlay-border-radius")).toBe("10px");
    expect(layer?.style.getPropertyValue("--card-overlay-edge-inset")).toBe("");

    unmount();
    expect(disconnect).toHaveBeenCalledOnce();
  });

  it("scales legacy browsers from the measured poster width", () => {
    vi.stubGlobal("CSS", { supports: () => false });
    const disconnect = observePosterWidth(92.5);

    const { container, unmount } = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("resolution", "square")} />,
    );
    const layer = container.querySelector<HTMLElement>('[data-card-overlays="poster"]');

    expect(layer?.style.getPropertyValue("--card-overlay-edge-inset")).toBe("4px");
    expect(layer?.style.getPropertyValue("--card-overlay-font-size")).toBe("4.5px");
    expect(layer?.style.getPropertyValue("--card-overlay-border-radius")).toBe("4px");
    expect(layer?.style.getPropertyValue("--card-overlay-border-left-width")).toBe("1px");

    unmount();
    expect(disconnect).toHaveBeenCalledOnce();
  });

  it("keeps the badge layer beneath card actions and non-interactive", () => {
    // MediaItemMenu renders its quick actions at z-20 in the card wrapper's
    // stacking context (see MediaItemMenu.test.tsx), so this z-10 layer paints
    // beneath them; pointer-events-none keeps a covered badge from swallowing
    // a click aimed at the action on top of it.
    const prefs = prefsWithOnly("content_rating");
    prefs.items.content_rating = { ...prefs.items.content_rating, position: "bottom-left" };
    const { container } = render(<CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />);

    for (const selector of ['[data-card-overlays="poster"]', '[data-overlay-edge="bottom"]']) {
      const node = container.querySelector<HTMLElement>(selector);
      expect(node, selector).toBeTruthy();
      expect(node?.classList.contains("z-10"), selector).toBe(true);
      expect(node?.classList.contains("pointer-events-none"), selector).toBe(true);
    }
    // Nothing inside the layer may re-enable hit testing.
    expect(container.querySelectorAll(".pointer-events-auto").length).toBe(0);
    expect(container.querySelector<HTMLElement>("span.inline-flex")?.style.pointerEvents).toBe("");
  });

  describe("request status", () => {
    const attention: OverlayData = {
      request_status: "Not found yet",
      request_status_icon: "alert",
      request_status_attention: true,
    };

    function badgeStyle(data: OverlayData, prefs: CardOverlayPrefs): string | null {
      return (
        render(<CardOverlays data={data} prefs={prefs} />)
          .container.querySelector<HTMLElement>("[data-overlay-badge]")
          ?.getAttribute("style") ?? null
      );
    }

    it.each(PRESET_IDS)("colors a status that needs attention amber in the %s preset", (id) => {
      const accented = prefsWithOnly("request_status", id);
      accented.items.request_status = {
        ...accented.items.request_status,
        accentColor: ATTENTION_ACCENT,
      };
      const calm = { ...attention, request_status_attention: false };
      expect(badgeStyle(attention, prefsWithOnly("request_status", id))).toBe(
        badgeStyle(calm, accented),
      );
      expect(badgeStyle(calm, prefsWithOnly("request_status", id))).not.toBe(
        badgeStyle(calm, accented),
      );
    });

    it("lets the viewer's own accent win over the attention accent", () => {
      const custom = prefsWithOnly("request_status", "square");
      custom.items.request_status = { ...custom.items.request_status, accentColor: "#3b82f6" };
      expect(badgeStyle(attention, custom)).toBe(
        badgeStyle({ ...attention, request_status_attention: false }, custom),
      );
    });
  });
});
