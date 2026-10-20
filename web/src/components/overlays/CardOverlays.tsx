import { useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";

import { useLanguageNamesLoaded } from "@/lib/languageNamesLoader";
import {
  OverlayIcon,
  WORDMARK_TEXT,
  applyOverlayStyle,
  getPreset,
  orderedOverlaysForPosition,
  overlayImageUrl,
  readableTextOn,
  watchStatusOf,
} from "@/lib/overlays";
import { cn } from "@/lib/utils";
import type {
  CardOverlayPrefs,
  OverlayData,
  OverlayDef,
  OverlayIconId,
  OverlayItemConfig,
  OverlayPosition,
  OverlayPreset,
  OverlayShadow,
} from "@/lib/overlays";

import {
  EDGE_GAP,
  EDGE_INSET,
  POSTER_REFERENCE_WIDTH,
  WATCH_STATUS_SIZE,
  overlayLength,
  setLegacyLength,
  supportsContainerQueryUnits,
  type ScalingMode,
} from "./overlayScale";

// Corner stacks render at most this many badges; anything further down the
// user's order is dropped rather than colliding with the opposite corner.
// Edge rails run along the card's height and hold one more.
const MAX_BADGES_PER_CORNER = 3;
const MAX_BADGES_PER_RAIL = 4;

// An uploaded image that replaces a whole badge is this many times the
// height of the text badge it stands in for, so a logo reads as a logo.
const VALUE_IMAGE_SCALE = 1.6;

const POSTER_VARS = {
  edgeInset: "--card-overlay-edge-inset",
  edgeGap: "--card-overlay-edge-gap",
  statusReserve: "--card-overlay-status-reserve",
  stackGap: "--card-overlay-stack-gap",
  fontSize: "--card-overlay-font-size",
  paddingInline: "--card-overlay-padding-inline",
  paddingBlock: "--card-overlay-padding-block",
  borderRadius: "--card-overlay-border-radius",
  borderWidth: "--card-overlay-border-width",
  borderLeftWidth: "--card-overlay-border-left-width",
  iconSize: "--card-overlay-icon-size",
  iconGap: "--card-overlay-icon-gap",
  imageHeight: "--card-overlay-image-height",
  textShadowX: "--card-overlay-text-shadow-x",
  textShadowY: "--card-overlay-text-shadow-y",
  textShadowBlur: "--card-overlay-text-shadow-blur",
  boxShadowX: "--card-overlay-box-shadow-x",
  boxShadowY: "--card-overlay-box-shadow-y",
  boxShadowBlur: "--card-overlay-box-shadow-blur",
  boxShadowSpread: "--card-overlay-box-shadow-spread",
} as const;

function valueImageHeight(preset: OverlayPreset): number {
  return (preset.fontSize + preset.paddingBlock * 2) * VALUE_IMAGE_SCALE;
}

// How far the top badge row stops short of the right edge: the watch-status
// corner plus the usual gap.
const STATUS_RESERVE = WATCH_STATUS_SIZE + EDGE_GAP;

function shadowValue(
  shadow: OverlayShadow,
  mode: ScalingMode,
  variables: { x: string; y: string; blur: string; spread?: string },
): string {
  const lengths = [
    overlayLength(shadow.x, mode, variables.x),
    overlayLength(shadow.y, mode, variables.y),
    overlayLength(shadow.blur, mode, variables.blur),
  ];
  if (variables.spread) {
    lengths.push(overlayLength(shadow.spread ?? 0, mode, variables.spread));
  }
  return [...lengths, shadow.color].join(" ");
}

function applyLegacyPosterScale(
  element: HTMLElement,
  width: number,
  preset: OverlayPreset,
  borderRadius: number,
) {
  if (width <= 0) return;
  const scale = width / POSTER_REFERENCE_WIDTH;

  setLegacyLength(element, POSTER_VARS.edgeInset, EDGE_INSET, scale);
  setLegacyLength(element, POSTER_VARS.edgeGap, EDGE_GAP, scale);
  setLegacyLength(element, POSTER_VARS.statusReserve, STATUS_RESERVE, scale);
  setLegacyLength(element, POSTER_VARS.stackGap, preset.stackGap, scale);
  setLegacyLength(element, POSTER_VARS.fontSize, preset.fontSize, scale);
  setLegacyLength(element, POSTER_VARS.paddingInline, preset.paddingInline, scale);
  setLegacyLength(element, POSTER_VARS.paddingBlock, preset.paddingBlock, scale);
  setLegacyLength(element, POSTER_VARS.iconSize, preset.iconSize, scale);
  setLegacyLength(element, POSTER_VARS.iconGap, preset.iconGap, scale);
  setLegacyLength(element, POSTER_VARS.imageHeight, valueImageHeight(preset), scale);
  if (preset.borderRadius !== "full") {
    setLegacyLength(element, POSTER_VARS.borderRadius, borderRadius, scale);
  }
  if (preset.borderWidth !== undefined) {
    setLegacyLength(element, POSTER_VARS.borderWidth, preset.borderWidth, scale);
  }
  if (preset.borderLeftWidth !== undefined) {
    setLegacyLength(element, POSTER_VARS.borderLeftWidth, preset.borderLeftWidth, scale);
  }
  if (preset.textShadow) {
    setLegacyLength(element, POSTER_VARS.textShadowX, preset.textShadow.x, scale);
    setLegacyLength(element, POSTER_VARS.textShadowY, preset.textShadow.y, scale);
    setLegacyLength(element, POSTER_VARS.textShadowBlur, preset.textShadow.blur, scale);
  }
  if (preset.boxShadow) {
    setLegacyLength(element, POSTER_VARS.boxShadowX, preset.boxShadow.x, scale);
    setLegacyLength(element, POSTER_VARS.boxShadowY, preset.boxShadow.y, scale);
    setLegacyLength(element, POSTER_VARS.boxShadowBlur, preset.boxShadow.blur, scale);
    setLegacyLength(element, POSTER_VARS.boxShadowSpread, preset.boxShadow.spread ?? 0, scale);
  }
}

interface CardOverlaysProps {
  data: OverlayData;
  prefs: CardOverlayPrefs;
  variant?: "poster" | "wide";
  /**
   * Set by hosts that draw a watch-progress bar along the bottom of the
   * artwork (ContinueWatchingCard, EpisodeRow, SeasonEpisodeGrid). The bar
   * occupies the same edge strip as the bottom badge row, so the row rises
   * just clear of it; with no bar the badges stay flush in the corners.
   */
  hasProgressBar?: boolean;
}

interface ResolvedBadge {
  def: OverlayDef;
  label: string;
  accentColor: string | undefined;
  iconId: OverlayIconId | null;
  // Uploaded image refs: one in the icon's place, or one for the whole badge.
  image: string | null;
  valueImage: string | null;
  valueKey: string | null;
  // The rating's source mark and bare score, when the preset draws chips or
  // an uploaded image already shows the source.
  sourceMark: string | null;
  score: string | null;
}

function resolveBadge(
  def: OverlayDef,
  data: OverlayData,
  preset: OverlayPreset,
  config: OverlayItemConfig | undefined,
): ResolvedBadge | null {
  const label = def.getValue(data);
  if (!label) return null;
  const dynamicIcon = def.getIcon ? def.getIcon(data) : null;
  const candidateIcon = dynamicIcon ?? def.iconId ?? null;
  const showIcon =
    def.iconCapable && candidateIcon !== null && (config?.showIcon ?? preset.preferIcon);
  const valueKey = def.getValueKey?.(data) ?? null;
  const score = def.getScore?.(data) ?? null;
  return {
    def,
    label,
    // The viewer's own accent wins; a data-driven accent (amber for a title
    // that needs attention) comes before the badge's default.
    accentColor: config?.accentColor ?? def.getAccent?.(data) ?? def.defaultAccent,
    iconId: showIcon ? candidateIcon : null,
    image: config?.image ?? null,
    valueImage: (valueKey !== null && config?.valueImages?.[valueKey]) || null,
    valueKey,
    sourceMark: def.sourceMark && score !== null ? def.sourceMark : null,
    score,
  };
}

// A wordmark icon (HDR10, ATMOS, ...) spells its text as the mark itself; when
// the label says the same thing, showing both reads "HDR10 HDR10".
function labelRedundantWithIcon(badge: ResolvedBadge): boolean {
  if (!badge.iconId) return false;
  const mark = WORDMARK_TEXT[badge.iconId];
  return mark !== undefined && mark.toLowerCase() === badge.label.trim().toLowerCase();
}

type Rail = "left" | "right";

interface BadgeProps {
  badge: ResolvedBadge;
  preset: OverlayPreset;
  scalingMode: ScalingMode;
  rail?: Rail;
}

// An administrator's uploaded image. A deleted image (404) falls back to
// whatever the badge draws without it.
function UploadedImage({
  imageRef,
  alt,
  height,
  onError,
}: {
  imageRef: string;
  alt: string;
  height: string;
  onError: () => void;
}) {
  return (
    <img
      src={overlayImageUrl(imageRef)}
      alt={alt}
      draggable={false}
      decoding="async"
      onError={onError}
      className="block max-w-full shrink-0 object-contain"
      style={{ height, width: "auto" }}
    />
  );
}

function Badge({ badge, preset, scalingMode, rail }: BadgeProps) {
  const [failedImages, setFailedImages] = useState<ReadonlySet<string>>(() => new Set());
  const failed = (ref: string) => setFailedImages((current) => new Set(current).add(ref));
  const length = (pixels: number, legacyVariable: string) =>
    overlayLength(pixels, scalingMode, legacyVariable);
  const dataAttributes = {
    "data-overlay-badge": "",
    "data-overlay-id": badge.def.id,
    "data-overlay-value": badge.valueKey ?? undefined,
  };

  if (badge.valueImage && !failedImages.has(badge.valueImage)) {
    return (
      <span {...dataAttributes} className="inline-flex max-w-full items-center">
        <UploadedImage
          imageRef={badge.valueImage}
          alt={badge.label}
          height={length(valueImageHeight(preset), POSTER_VARS.imageHeight)}
          onError={() => failed(badge.valueImage as string)}
        />
      </span>
    );
  }

  const borderRadiusVariable = preset.borderRadiusVariable ?? "--radius-sm";
  const radius =
    preset.borderRadius === "full"
      ? "9999px"
      : scalingMode === "fixed"
        ? `var(${borderRadiusVariable}, ${preset.borderRadius}px)`
        : `var(${POSTER_VARS.borderRadius}, var(${borderRadiusVariable}, ${preset.borderRadius}px))`;
  const geometry: CSSProperties = {
    columnGap: length(preset.iconGap, POSTER_VARS.iconGap),
    paddingInline: length(preset.paddingInline, POSTER_VARS.paddingInline),
    paddingBlock: length(preset.paddingBlock, POSTER_VARS.paddingBlock),
    // The inline size beats the preset's rem class, so it carries the
    // text size itself: browser default and in-app scale (#1796).
    fontSize: `calc(${length(preset.fontSize, POSTER_VARS.fontSize)} * var(--ui-root-text-ratio, 1))`,
    fontWeight: preset.fontWeight,
    textTransform: preset.uppercase ? "uppercase" : "none",
  };
  if (rail) {
    // A badge on an edge rail is a tab: flush against the card edge, square
    // on that side, rounded on the side facing the artwork. Longhands only,
    // so React never mixes them with the borderRadius shorthand.
    const outer = rail === "left" ? "Left" : "Right";
    const inner = rail === "left" ? "Right" : "Left";
    geometry[`borderTop${inner}Radius`] = radius;
    geometry[`borderBottom${inner}Radius`] = radius;
    geometry[`borderTop${outer}Radius`] = 0;
    geometry[`borderBottom${outer}Radius`] = 0;
  } else {
    geometry.borderRadius = radius;
  }
  if (preset.borderWidth !== undefined) {
    geometry.borderWidth = length(preset.borderWidth, POSTER_VARS.borderWidth);
    if (rail === "left") geometry.borderLeftWidth = 0;
    if (rail === "right") geometry.borderRightWidth = 0;
  }
  if (preset.borderLeftWidth !== undefined && badge.accentColor !== undefined && rail !== "left") {
    geometry.borderLeftWidth = length(preset.borderLeftWidth, POSTER_VARS.borderLeftWidth);
  }
  if (preset.textShadow) {
    geometry.textShadow = shadowValue(preset.textShadow, scalingMode, {
      x: POSTER_VARS.textShadowX,
      y: POSTER_VARS.textShadowY,
      blur: POSTER_VARS.textShadowBlur,
    });
  }
  if (preset.boxShadow) {
    geometry.boxShadow = shadowValue(preset.boxShadow, scalingMode, {
      x: POSTER_VARS.boxShadowX,
      y: POSTER_VARS.boxShadowY,
      blur: POSTER_VARS.boxShadowBlur,
      spread: POSTER_VARS.boxShadowSpread,
    });
  }

  const iconImage = badge.image && !failedImages.has(badge.image) ? badge.image : null;
  const iconSize = length(preset.iconSize, POSTER_VARS.iconSize);
  // An uploaded image beside a rating stands for its source, so the label
  // drops the mark; the plate preset draws the mark as a chip instead.
  const sourceChip = !iconImage && preset.sourceChips && badge.sourceMark !== null;
  const scoreOnly = (iconImage !== null || sourceChip) && badge.score !== null;
  const text = scoreOnly ? badge.score : labelRedundantWithIcon(badge) ? null : badge.label;
  const chipColor = badge.accentColor ?? "#ffffff";

  // min-w-0 lets a band's first mark truncate when it is too wide on its own.
  return (
    <span
      {...dataAttributes}
      className={`inline-flex max-w-full min-w-0 items-center gap-1 ${preset.badgeClass}`}
      style={{ ...preset.badgeStyle(badge.accentColor), ...geometry }}
    >
      {iconImage ? (
        <UploadedImage
          imageRef={iconImage}
          alt=""
          height={iconSize}
          onError={() => failed(iconImage)}
        />
      ) : sourceChip ? (
        <span
          className="shrink-0 rounded-[0.2em] px-[0.35em] py-[0.15em] leading-none"
          style={{ background: chipColor, color: readableTextOn(chipColor) }}
        >
          {badge.sourceMark}
        </span>
      ) : (
        badge.iconId && (
          <OverlayIcon
            iconId={badge.iconId}
            size={preset.iconSize}
            cssSize={iconSize}
            className="shrink-0"
          />
        )
      )}
      {/* A cut score ("6.") misleads, so a bare score never truncates. */}
      {text !== null && <span className={scoreOnly ? "shrink-0" : "truncate"}>{text}</span>}
    </span>
  );
}

function BadgeStack({
  badges,
  align,
  preset,
  scalingMode,
  rail,
}: {
  badges: ResolvedBadge[];
  align: "start" | "end";
  preset: OverlayPreset;
  scalingMode: ScalingMode;
  rail?: Rail;
}) {
  return (
    <div
      className={`flex min-w-0 flex-col ${align === "start" ? "items-start" : "items-end"} ${preset.gapClass}`}
      style={{ gap: overlayLength(preset.stackGap, scalingMode, POSTER_VARS.stackGap) }}
    >
      {badges.map((badge) => (
        <Badge
          key={badge.def.id}
          badge={badge}
          preset={preset}
          scalingMode={scalingMode}
          rail={rail}
        />
      ))}
    </div>
  );
}

// The band layout draws an edge's badges as one row of marks with thin
// dividers between them. The row shows the marks that fit whole, in the
// viewer's order, and drops the rest, as a corner drops badges past its cap:
// a cut mark ("AT…") says nothing. Only a first mark too wide on its own
// truncates.
function BandMarks({
  badges,
  preset,
  scalingMode,
}: {
  badges: ResolvedBadge[];
  preset: OverlayPreset;
  scalingMode: ScalingMode;
}) {
  const rowRef = useRef<HTMLDivElement>(null);
  const gap = overlayLength(preset.stackGap, scalingMode, POSTER_VARS.stackGap);
  const marksKey = badges.map((badge) => `${badge.def.id}:${badge.label}`).join("|");

  useLayoutEffect(() => {
    const row = rowRef.current;
    const units = Array.from(row?.children ?? []) as HTMLElement[];
    const [first] = units;
    if (!row || !first || units.length < 2) return;
    const fit = () => {
      // Measure every mark at its full width.
      for (const unit of units) unit.style.display = "";
      first.style.flexShrink = "0";
      // Half a pixel absorbs subpixel rounding of the measured edges.
      const limit = row.getBoundingClientRect().right + 0.5;
      const firstDropped = units.findIndex(
        (unit, index) => index > 0 && unit.getBoundingClientRect().right > limit,
      );
      if (firstDropped === -1) return;
      for (const unit of units.slice(firstDropped)) unit.style.display = "none";
      // The first mark may shrink, and truncate, once it is alone.
      if (firstDropped === 1) first.style.flexShrink = "";
    };
    fit();
    if (typeof ResizeObserver === "undefined") return;
    // The marks resize too, when the badge font or an uploaded image loads.
    const observer = new ResizeObserver(fit);
    observer.observe(row);
    for (const unit of units) observer.observe(unit);
    return () => observer.disconnect();
  }, [marksKey, preset, scalingMode]);

  return (
    <div ref={rowRef} className="flex min-w-0 items-center" style={{ gap }}>
      {badges.map((badge, index) => (
        <span
          key={badge.def.id}
          className={cn("flex items-center", index === 0 ? "min-w-0" : "shrink-0")}
          style={{ gap }}
        >
          {index > 0 && (
            <span
              aria-hidden="true"
              className="h-[0.9em] w-px shrink-0 bg-white/40"
              style={{
                fontSize: overlayLength(preset.fontSize, scalingMode, POSTER_VARS.fontSize),
              }}
            />
          )}
          <Badge badge={badge} preset={preset} scalingMode={scalingMode} />
        </span>
      ))}
    </div>
  );
}

// Each card edge renders as ONE flex row holding the corner stacks on that
// edge. Sharing a row lets flexbox divide the card width between opposing
// corners (min-w-0 + truncate), so wide badges shrink instead of overlapping.
// The top row holds only the left corner and stops short of the top-right
// corner, which belongs to the watch-status indicator (CardWatchStatus).
// Badges placed on an edge rail stack vertically, centred on that side.
//
// Stacking contract with the card quick actions (watched/favorite bottom-left,
// more-menu bottom-right; web/src/components/MediaItemMenu.tsx): this whole
// layer paints at z-10 inside the artwork box, the action wrappers paint at
// z-20 as siblings of the artwork link in the same stacking context, so the
// actions always cover overlay badges. That is by design — badges sit flush in
// the corners and the actions (hover-revealed on fine pointers, persistent on
// coarse ones) render on top of them. The layer is pointer-events-none end to
// end, so a badge can never swallow a click aimed at an action underneath it.
export default function CardOverlays({
  data,
  prefs,
  variant = "poster",
  hasProgressBar = false,
}: CardOverlaysProps) {
  // The language badge resolves names from lazily loaded data and stays empty
  // until it arrives; this re-renders the card when it does. Cards without the
  // badge skip the re-render.
  useLanguageNamesLoaded(prefs.items.original_language?.enabled === true);
  const preset = useMemo(
    () => applyOverlayStyle(getPreset(prefs.preset), prefs.style),
    [prefs.preset, prefs.style],
  );
  const resolve = (pos: OverlayPosition, limit: number): ResolvedBadge[] =>
    orderedOverlaysForPosition(prefs, pos)
      .map((def) => resolveBadge(def, data, preset, prefs.items[def.id]))
      .filter((badge): badge is ResolvedBadge => badge !== null)
      .slice(0, limit);

  const topLeft = resolve("top-left", MAX_BADGES_PER_CORNER);
  const bottomLeft = resolve("bottom-left", MAX_BADGES_PER_CORNER);
  const bottomRight = resolve("bottom-right", MAX_BADGES_PER_CORNER);
  const leftRail = resolve("left-edge", MAX_BADGES_PER_RAIL);
  const rightRail = resolve("right-edge", MAX_BADGES_PER_RAIL);
  const wide = variant === "wide";
  const band = preset.layout === "band";
  const scalingMode: ScalingMode = wide
    ? "fixed"
    : supportsContainerQueryUnits()
      ? "container"
      : "legacy";
  const layerRef = useRef<HTMLDivElement>(null);
  const edgeInset = overlayLength(EDGE_INSET, scalingMode, POSTER_VARS.edgeInset);
  const edgeGap = overlayLength(EDGE_GAP, scalingMode, POSTER_VARS.edgeGap);
  // Poster cards keep the top-right corner clear for the watch-status
  // indicator unless the viewer turned it off; wide cards never draw it.
  const reserveStatus = !wide && watchStatusOf(prefs) !== "off";
  const topRight = reserveStatus
    ? `calc(${edgeInset} + ${overlayLength(STATUS_RESERVE, scalingMode, POSTER_VARS.statusReserve)})`
    : edgeInset;
  // A filled badge on a rail sits flush against the edge as a tab; a
  // transparent one (minimal, band) keeps the corner inset.
  const flushRails = !band && preset.id !== "minimal";
  const railInset = flushRails ? 0 : edgeInset;

  useLayoutEffect(() => {
    const layer = layerRef.current;
    const roundedPoster = scalingMode !== "fixed" && preset.borderRadius !== "full";
    if (!layer || (scalingMode !== "legacy" && !roundedPoster)) {
      return;
    }

    let baseBorderRadius = preset.borderRadius === "full" ? 0 : preset.borderRadius;
    if (roundedPoster) {
      layer.style.removeProperty(POSTER_VARS.borderRadius);
      const badge = layer.querySelector<HTMLElement>("span[data-overlay-badge][class*='rounded']");
      if (badge) {
        // A rail tab squares one side, so the larger top corner is the radius.
        const computed = getComputedStyle(badge);
        const measured = [computed.borderTopLeftRadius, computed.borderTopRightRadius]
          .map((value) => Number.parseFloat(value ?? ""))
          .filter(Number.isFinite);
        if (measured.length > 0) baseBorderRadius = Math.max(...measured);
      }
    }

    if (typeof ResizeObserver === "undefined") return;
    const update = (width: number) => {
      if (scalingMode === "legacy") {
        applyLegacyPosterScale(layer, width, preset, baseBorderRadius);
      } else {
        setLegacyLength(
          layer,
          POSTER_VARS.borderRadius,
          baseBorderRadius,
          width / POSTER_REFERENCE_WIDTH,
        );
      }
    };
    update(layer.getBoundingClientRect().width);
    const observer = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry) update(entry.contentRect.width);
    });
    observer.observe(layer);
    return () => observer.disconnect();
  }, [preset, scalingMode]);

  const bandScrim = (edge: "top" | "bottom"): CSSProperties => ({
    background: `linear-gradient(to ${edge === "top" ? "bottom" : "top"}, rgb(0 0 0 / 0.85), rgb(0 0 0 / 0.6) 55%, transparent)`,
  });

  return (
    <div
      ref={layerRef}
      data-card-overlays={variant}
      className="@container/card-overlays pointer-events-none absolute inset-0 z-10"
    >
      {topLeft.length > 0 &&
        (band ? (
          <div
            data-overlay-edge="top"
            className="pointer-events-none absolute inset-x-0 top-0 z-10 flex items-start"
            style={{
              ...bandScrim("top"),
              paddingTop: edgeInset,
              paddingLeft: edgeInset,
              paddingRight: topRight,
              paddingBottom: `calc(${edgeInset} * 3)`,
            }}
          >
            <BandMarks badges={topLeft} preset={preset} scalingMode={scalingMode} />
          </div>
        ) : (
          <div
            data-overlay-edge="top"
            className="pointer-events-none absolute inset-x-2 top-2 z-10 flex items-start justify-start gap-2"
            style={{ top: edgeInset, right: topRight, left: edgeInset, gap: edgeGap }}
          >
            <BadgeStack badges={topLeft} align="start" preset={preset} scalingMode={scalingMode} />
          </div>
        ))}
      {leftRail.length > 0 && (
        <div
          data-overlay-edge="left"
          className="pointer-events-none absolute top-1/2 z-10 flex max-w-[60%] -translate-y-1/2"
          style={{ left: railInset }}
        >
          <BadgeStack
            badges={leftRail}
            align="start"
            preset={preset}
            scalingMode={scalingMode}
            rail={flushRails ? "left" : undefined}
          />
        </div>
      )}
      {rightRail.length > 0 && (
        <div
          data-overlay-edge="right"
          className="pointer-events-none absolute top-1/2 z-10 flex max-w-[60%] -translate-y-1/2 justify-end"
          style={{ right: railInset }}
        >
          <BadgeStack
            badges={rightRail}
            align="end"
            preset={preset}
            scalingMode={scalingMode}
            rail={flushRails ? "right" : undefined}
          />
        </div>
      )}
      {(bottomLeft.length > 0 || bottomRight.length > 0) &&
        (band ? (
          /* One centred strip for the whole bottom edge, left corner first. */
          <div
            data-overlay-edge="bottom"
            className="pointer-events-none absolute inset-x-0 bottom-0 z-10 flex justify-center"
            style={{
              ...bandScrim("bottom"),
              paddingTop: `calc(${edgeInset} * 3)`,
              paddingInline: edgeInset,
              paddingBottom: hasProgressBar ? `calc(${edgeInset} * 2)` : edgeInset,
            }}
          >
            <BandMarks
              badges={[...bottomLeft, ...bottomRight]}
              preset={preset}
              scalingMode={scalingMode}
            />
          </div>
        ) : (
          /* Bottom badges anchor flush in the corners with the same inset as
             the top row; card actions cover them by design (see above). The
             single exception is a watch-progress bar, which lives in this same
             edge strip and would be cut in half by a flush badge, so hosts
             that draw one lift the row by one edge step. */
          <div
            data-overlay-edge="bottom"
            className={cn(
              "pointer-events-none absolute inset-x-2 bottom-2 z-10 flex items-end justify-between gap-2",
              hasProgressBar && "mb-2",
            )}
            style={{ right: edgeInset, bottom: edgeInset, left: edgeInset, gap: edgeGap }}
          >
            <BadgeStack
              badges={bottomLeft}
              align="start"
              preset={preset}
              scalingMode={scalingMode}
            />
            <BadgeStack
              badges={bottomRight}
              align="end"
              preset={preset}
              scalingMode={scalingMode}
            />
          </div>
        ))}
    </div>
  );
}
