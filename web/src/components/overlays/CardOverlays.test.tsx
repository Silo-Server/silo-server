import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";

import CardOverlays from "./CardOverlays";
import {
  OVERLAY_PRESETS,
  OVERLAY_REGISTRY,
  PRESET_IDS,
  SAMPLE_MOVIE_DATA,
  SAMPLE_SHOW_DATA,
  buildDefaultPrefs,
  type CardOverlayPrefs,
  type OverlayId,
  type PresetId,
} from "@/lib/overlays";

const posterLength = (pixels: number) => `${Number(((pixels / 185) * 100).toFixed(6))}cqi`;

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

/** Whole-token match so a mangled class ("gap-2mb-2") can never pass as flush. */
function bottomMarginClasses(node: HTMLElement | null): string[] {
  return Array.from(node?.classList ?? []).filter((name) => /^mb-/.test(name));
}

describe("CardOverlays", () => {
  beforeEach(() => vi.stubGlobal("CSS", { supports: () => true }));
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

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

  it("scales poster badge geometry from the card width and keeps wide badges at baseline size", () => {
    const poster = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("audio", "pill")} />,
    ).container;
    const posterLayer = poster.querySelector<HTMLElement>('[data-card-overlays="poster"]');
    const posterTop = poster.querySelector<HTMLElement>('[data-overlay-edge="top"]');
    const posterBadge = poster.querySelector<HTMLElement>("span.inline-flex");
    const posterIcon = posterBadge?.querySelector<SVGElement>("svg");

    expect(posterLayer?.className).toContain("@container/card-overlays");
    expect(posterTop?.style.left).toBe(posterLength(8));
    expect(posterTop?.style.top).toBe(posterLength(8));
    expect(posterBadge?.style.fontSize).toBe(posterLength(10));
    expect(posterBadge?.style.paddingInline).toBe(posterLength(10));
    expect(posterBadge?.style.paddingBlock).toBe(posterLength(4));
    expect(posterBadge?.style.borderWidth).toBe(posterLength(1));
    expect(posterIcon?.style.height).toBe(posterLength(12));
    expect(posterIcon?.getAttribute("height")).toBe("12");

    const wide = render(
      <CardOverlays
        data={SAMPLE_MOVIE_DATA}
        prefs={prefsWithOnly("audio", "pill")}
        variant="wide"
      />,
    ).container;
    const wideTop = wide.querySelector<HTMLElement>('[data-overlay-edge="top"]');
    const wideBadge = wide.querySelector<HTMLElement>("span.inline-flex");
    const wideIcon = wideBadge?.querySelector<SVGElement>("svg");

    expect(wideTop?.style.left).toBe("8px");
    expect(wideTop?.style.top).toBe("8px");
    expect(wideBadge?.style.fontSize).toBe("10px");
    expect(wideBadge?.style.paddingInline).toBe("10px");
    expect(wideBadge?.style.paddingBlock).toBe("4px");
    expect(wideIcon?.style.height).toBe("12px");
    expect(wideIcon?.getAttribute("height")).toBe("12");
  });

  it.each(PRESET_IDS)("keeps the %s preset proportional with fixed geometry fallbacks", (id) => {
    const preset = OVERLAY_PRESETS[id];
    const container = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("resolution", id)} />,
    ).container;
    const stack = container.querySelector<HTMLElement>(
      '[data-overlay-edge="top"] > div.items-start',
    );
    const badge = container.querySelector<HTMLElement>("span.inline-flex");

    expect(stack?.className).toContain(preset.gapClass);
    expect(stack?.style.gap).toBe(posterLength(preset.stackGap));
    expect(badge?.className).toContain(preset.badgeClass);
    expect(badge?.style.columnGap).toBe(posterLength(preset.iconGap));
    expect(badge?.style.fontSize).toBe(posterLength(preset.fontSize));
    expect(badge?.style.paddingInline).toBe(posterLength(preset.paddingInline));
    expect(badge?.style.paddingBlock).toBe(posterLength(preset.paddingBlock));
    expect(badge?.style.borderRadius).toBe(
      preset.borderRadius === "full"
        ? "9999px"
        : `var(--card-overlay-border-radius, var(${preset.borderRadiusVariable}, ${preset.borderRadius}px))`,
    );
    expect(badge?.style.borderWidth).toBe(
      preset.borderWidth === undefined ? "" : posterLength(preset.borderWidth),
    );
    // CSSStyleDeclaration exposes an all-sides borderWidth through its side longhands.
    expect(badge?.style.borderLeftWidth).toBe(
      preset.borderWidth === undefined ? "" : posterLength(preset.borderWidth),
    );
    expect(badge?.style.textShadow).toBe(
      preset.textShadow === undefined
        ? ""
        : `${posterLength(preset.textShadow.x)} ${posterLength(preset.textShadow.y)} ${posterLength(preset.textShadow.blur)} ${preset.textShadow.color}`,
    );
    expect(badge?.style.boxShadow).toBe(
      preset.boxShadow === undefined
        ? ""
        : `${posterLength(preset.boxShadow.x)} ${posterLength(preset.boxShadow.y)} ${posterLength(preset.boxShadow.blur)} ${posterLength(preset.boxShadow.spread ?? 0)} ${preset.boxShadow.color}`,
    );
  });

  it("only scales the square accent border when the badge has an accent", () => {
    const plain = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("resolution", "square")} />,
    ).container.querySelector<HTMLElement>("span.inline-flex");
    expect(plain?.style.borderLeftWidth).toBe("");

    const prefs = prefsWithOnly("resolution", "square");
    prefs.items.resolution = { ...prefs.items.resolution, accentColor: "#f5c518" };
    const accented = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />,
    ).container.querySelector<HTMLElement>("span.inline-flex");
    expect(accented?.style.borderLeftWidth).toBe(posterLength(2));
  });

  it("scales the active theme radius with a rounded poster preset", () => {
    let callback: ResizeObserverCallback | undefined;
    const disconnect = vi.fn();
    vi.spyOn(window, "getComputedStyle").mockReturnValue({
      borderTopLeftRadius: "20px",
    } as CSSStyleDeclaration);
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(next: ResizeObserverCallback) {
          callback = next;
        }
        observe(target: Element) {
          callback?.(
            [{ target, contentRect: { width: 92.5 } } as unknown as ResizeObserverEntry],
            this as unknown as ResizeObserver,
          );
        }
        disconnect = disconnect;
      },
    );

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
    let callback: ResizeObserverCallback | undefined;
    const disconnect = vi.fn();
    vi.stubGlobal("CSS", { supports: () => false });
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(next: ResizeObserverCallback) {
          callback = next;
        }
        observe(target: Element) {
          callback?.(
            [{ target, contentRect: { width: 92.5 } } as unknown as ResizeObserverEntry],
            this as unknown as ResizeObserver,
          );
        }
        disconnect = disconnect;
      },
    );

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

  it("scales preset shadows from the measured poster width in legacy browsers", () => {
    const disconnect = vi.fn();
    vi.stubGlobal("CSS", { supports: () => false });
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(private callback: ResizeObserverCallback) {}
        observe(target: Element) {
          this.callback(
            [{ target, contentRect: { width: 92.5 } } as unknown as ResizeObserverEntry],
            this as unknown as ResizeObserver,
          );
        }
        disconnect = disconnect;
      },
    );

    const { container, unmount } = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefsWithOnly("resolution", "minimal")} />,
    );
    const layer = container.querySelector<HTMLElement>('[data-card-overlays="poster"]');

    expect(layer?.style.getPropertyValue("--card-overlay-text-shadow-x")).toBe("0px");
    expect(layer?.style.getPropertyValue("--card-overlay-text-shadow-y")).toBe("0.5px");
    expect(layer?.style.getPropertyValue("--card-overlay-text-shadow-blur")).toBe("1px");

    unmount();
    expect(disconnect).toHaveBeenCalledOnce();
  });

  it("keeps preset shadows at baseline size on wide cards", () => {
    const minimal = render(
      <CardOverlays
        data={SAMPLE_MOVIE_DATA}
        prefs={prefsWithOnly("resolution", "minimal")}
        variant="wide"
      />,
    ).container.querySelector<HTMLElement>("span.inline-flex");
    expect(minimal?.style.textShadow).toBe("0px 1px 2px rgba(0,0,0,0.85)");

    const vibrant = render(
      <CardOverlays
        data={SAMPLE_MOVIE_DATA}
        prefs={prefsWithOnly("resolution", "vibrant")}
        variant="wide"
      />,
    ).container.querySelector<HTMLElement>("span.inline-flex");
    expect(vibrant?.style.boxShadow).toBe("0px 1px 2px 0px rgb(0 0 0 / 0.25)");
  });

  it("anchors bottom-corner badges flush in the corners like the top row", () => {
    // Card quick actions cover bottom badges by design, so the bottom row
    // reserves no clearance for them and insets exactly like the top row.
    const prefs = prefsWithOnly("content_rating");
    const bottomEdge = (variant?: "wide") => {
      prefs.items.content_rating = { ...prefs.items.content_rating, position: "bottom-left" };
      const left = render(
        <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} variant={variant} />,
      ).container;
      prefs.items.content_rating = { ...prefs.items.content_rating, position: "bottom-right" };
      const right = render(
        <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} variant={variant} />,
      ).container;
      return {
        leftStack: left.querySelector<HTMLElement>(
          '[data-overlay-edge="bottom"] > div.items-start',
        ),
        rightStack: right.querySelector<HTMLElement>(
          '[data-overlay-edge="bottom"] > div.items-end',
        ),
        row: left.querySelector<HTMLElement>('[data-overlay-edge="bottom"]'),
      };
    };

    for (const variant of [undefined, "wide" as const]) {
      const { leftStack, rightStack, row } = bottomEdge(variant);
      expect(leftStack).toBeTruthy();
      expect(rightStack).toBeTruthy();
      for (const node of [leftStack, rightStack, row]) {
        expect(bottomMarginClasses(node), `${variant ?? "poster"} bottom edge`).toEqual([]);
      }
    }

    prefs.items.content_rating = { ...prefs.items.content_rating, position: "top-left" };
    const top = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />,
    ).container.querySelector<HTMLElement>('[data-overlay-edge="top"]');
    prefs.items.content_rating = { ...prefs.items.content_rating, position: "bottom-left" };
    const bottom = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />,
    ).container.querySelector<HTMLElement>('[data-overlay-edge="bottom"]');
    expect(top?.style.top).toBe(posterLength(8));
    expect(bottom?.style.bottom).toBe(top?.style.top);
  });

  it("lifts the bottom row only when the host draws a watch-progress bar", () => {
    // The bar occupies the same edge strip, so a flush badge would cut it.
    const prefs = prefsWithOnly("content_rating");
    prefs.items.content_rating = { ...prefs.items.content_rating, position: "bottom-left" };
    const flush = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} />,
    ).container.querySelector<HTMLElement>('[data-overlay-edge="bottom"]');
    expect(bottomMarginClasses(flush)).toEqual([]);

    const lifted = render(
      <CardOverlays data={SAMPLE_MOVIE_DATA} prefs={prefs} hasProgressBar />,
    ).container.querySelector<HTMLElement>('[data-overlay-edge="bottom"]');
    expect(bottomMarginClasses(lifted)).toEqual(["mb-2"]);
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

  it("renders nothing when no enabled overlay has data", () => {
    const { container } = render(<CardOverlays data={{}} prefs={buildDefaultPrefs()} />);
    expect(container.querySelectorAll("span.inline-flex").length).toBe(0);
  });
});
