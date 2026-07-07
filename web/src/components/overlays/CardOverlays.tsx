import { OverlayIcon, WORDMARK_TEXT, getPreset, orderedOverlaysForPosition } from "@/lib/overlays";
import type {
  CardOverlayPrefs,
  OverlayData,
  OverlayDef,
  OverlayIconId,
  OverlayPosition,
  OverlayPreset,
} from "@/lib/overlays";

// Corner stacks render at most this many badges; anything further down the
// user's order is dropped rather than colliding with the opposite corner.
const MAX_BADGES_PER_CORNER = 3;

interface CardOverlaysProps {
  data: OverlayData;
  prefs: CardOverlayPrefs;
  variant?: "poster" | "wide";
}

interface ResolvedBadge {
  def: OverlayDef;
  label: string;
  accentColor: string | undefined;
  iconId: OverlayIconId | null;
}

function resolveBadge(
  def: OverlayDef,
  data: OverlayData,
  preset: OverlayPreset,
  itemAccent: string | undefined,
  itemShowIcon: boolean | undefined,
): ResolvedBadge | null {
  const label = def.getValue(data);
  if (!label) return null;
  const dynamicIcon = def.getIcon ? def.getIcon(data) : null;
  const candidateIcon = dynamicIcon ?? def.iconId ?? null;
  const showIcon = def.iconCapable && candidateIcon !== null && (itemShowIcon ?? preset.preferIcon);
  return {
    def,
    label,
    accentColor: itemAccent ?? def.defaultAccent,
    iconId: showIcon ? candidateIcon : null,
  };
}

// A wordmark icon (HDR10, ATMOS, ...) spells its text as the mark itself; when
// the label says the same thing, showing both reads "HDR10 HDR10".
function labelRedundantWithIcon(badge: ResolvedBadge): boolean {
  if (!badge.iconId) return false;
  const mark = WORDMARK_TEXT[badge.iconId];
  return mark !== undefined && mark.toLowerCase() === badge.label.trim().toLowerCase();
}

function BadgeStack({
  badges,
  align,
  preset,
  extraClass = "",
}: {
  badges: ResolvedBadge[];
  align: "start" | "end";
  preset: OverlayPreset;
  extraClass?: string;
}) {
  return (
    <div
      className={`flex min-w-0 flex-col ${align === "start" ? "items-start" : "items-end"} ${preset.gapClass} ${extraClass}`}
    >
      {badges.map((badge) => (
        <span
          key={badge.def.id}
          className={`inline-flex max-w-full items-center gap-1 ${preset.badgeClass}`}
          style={preset.badgeStyle(badge.accentColor)}
        >
          {badge.iconId && (
            <OverlayIcon iconId={badge.iconId} size={preset.iconSize} className="shrink-0" />
          )}
          {!labelRedundantWithIcon(badge) && <span className="truncate">{badge.label}</span>}
        </span>
      ))}
    </div>
  );
}

// Each card edge renders as ONE flex row holding the left and right corner
// stacks. Sharing a row lets flexbox divide the card width between opposing
// corners (min-w-0 + truncate), so wide badges shrink instead of overlapping.
export default function CardOverlays({ data, prefs, variant = "poster" }: CardOverlaysProps) {
  const preset = getPreset(prefs.preset);
  const resolve = (pos: OverlayPosition): ResolvedBadge[] =>
    orderedOverlaysForPosition(prefs, pos)
      .map((def) => {
        const config = prefs.items[def.id];
        return resolveBadge(def, data, preset, config?.accentColor, config?.showIcon);
      })
      .filter((badge): badge is ResolvedBadge => badge !== null)
      .slice(0, MAX_BADGES_PER_CORNER);

  const topLeft = resolve("top-left");
  const topRight = resolve("top-right");
  const bottomLeft = resolve("bottom-left");
  const bottomRight = resolve("bottom-right");
  const wide = variant === "wide";

  return (
    <>
      {(topLeft.length > 0 || topRight.length > 0) && (
        <div className="pointer-events-none absolute inset-x-2 top-2 z-10 flex items-start justify-between gap-2">
          <BadgeStack badges={topLeft} align="start" preset={preset} />
          <BadgeStack badges={topRight} align="end" preset={preset} />
        </div>
      )}
      {(bottomLeft.length > 0 || bottomRight.length > 0) && (
        <div className="pointer-events-none absolute inset-x-2 bottom-2 z-10 flex items-end justify-between gap-2">
          {/* Wide cards keep the bottom edge clear for the progress bar. */}
          <BadgeStack
            badges={bottomLeft}
            align="start"
            preset={preset}
            extraClass={wide ? "mb-4" : ""}
          />
          {/* The card menu button (MediaItemMenu) owns the bottom-right
              corner — always visible on touch devices — so this stack sits
              above it. */}
          <BadgeStack
            badges={bottomRight}
            align="end"
            preset={preset}
            extraClass={wide ? "mb-12" : "mb-10"}
          />
        </div>
      )}
    </>
  );
}
