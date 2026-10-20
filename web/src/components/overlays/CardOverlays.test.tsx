import { fireEvent, render } from "@testing-library/react";
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

  // jsdom resolves no CSS variables, so this reads where the radius comes from.
  it.each([
    { shape: undefined, themed: true },
    { shape: "rounded", themed: false },
    { shape: "square", themed: false },
  ] as const)(
    "takes badge corners from the theme unless the style picks a shape: $shape",
    ({ shape, themed }) => {
      const prefs = {
        ...prefsWithOnly("resolution", "minimal"),
        style: shape ? { shape } : undefined,
      };
      const badge = render(
        <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />,
      ).container.querySelector<HTMLElement>("span.inline-flex");
      expect(badge?.style.borderRadius.includes("--radius-")).toBe(themed);
    },
  );

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

  it.each([
    { id: "resolution", fill: "#FFFFFFE6", framed: true, color: "rgb(0, 0, 0)" },
    { id: "resolution", fill: "#000000B3", framed: true, color: "rgb(255, 255, 255)" },
    { id: "resolution", fill: "#00000000", framed: false, color: "white" },
    // IMDb gold stays as text where it reads and gives way where it doesn't.
    { id: "rating_imdb", fill: "#000000B3", framed: true, color: "rgb(245, 197, 24)" },
    { id: "rating_imdb", fill: "#FFFFFFE6", framed: true, color: "rgb(0, 0, 0)" },
  ] as const)(
    "gives bare band text room and a readable colour: $id on $fill",
    ({ id, fill, framed, color }) => {
      const prefs = { ...prefsWithOnly(id, "band"), style: { background: fill } };
      const badge = render(
        <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} variant="wide" />,
      ).container.querySelector<HTMLElement>("span.inline-flex");

      const padding = Number.parseFloat(badge?.style.paddingInline ?? "");
      expect(padding > 0).toBe(framed);
      expect(badge?.style.color).toBe(color);
      expect(badge?.style.textShadow === "").toBe(framed);
    },
  );

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

  it("replaces a badge with the uploaded image for its value, and falls back to text without it", () => {
    const ref = `${"ab".repeat(32)}.webp`;
    const prefs = prefsWithOnly("resolution");
    prefs.items.resolution = { ...prefs.items.resolution, valueImages: { "2160p": ref } };
    const { container } = render(<CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />);
    const image = container.querySelector<HTMLImageElement>("[data-overlay-badge] img");

    expect(image?.getAttribute("src")).toBe(`/api/v2/overlay-images/${ref}`);
    expect(image?.alt).toBe("4K");

    // A deleted image answers 404; the badge goes back to its label.
    fireEvent.error(image as HTMLImageElement);
    expect(container.querySelector("[data-overlay-badge] img")).toBeNull();
    expect(badgeTexts(container)).toEqual(["4K"]);
  });

  it("shows a rating's score beside its source chip or uploaded logo, not the mark twice", () => {
    const chip = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("rating_imdb", "plate")} />,
    ).container;
    expect(badgeTexts(chip)).toEqual(["IMDb8.7"]);

    const prefs = prefsWithOnly("rating_imdb");
    prefs.items.rating_imdb = { ...prefs.items.rating_imdb, image: `${"cd".repeat(32)}.webp` };
    const logo = render(<CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />).container;
    expect(badgeTexts(logo)).toEqual(["8.7"]);
    expect(logo.querySelector("[data-overlay-badge] img")).not.toBeNull();

    const plain = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("rating_imdb")} />,
    ).container;
    expect(badgeTexts(plain)).toEqual(["IMDb 8.7"]);
  });
});
