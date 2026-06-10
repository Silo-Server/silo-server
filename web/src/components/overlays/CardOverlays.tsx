import {
  OVERLAY_POSITIONS,
  OVERLAY_REGISTRY,
  OverlayIcon,
  getPreset,
  isOverlaySuppressed,
} from "@/lib/overlays";
import type {
  CardOverlayPrefs,
  OverlayData,
  OverlayDef,
  OverlayIconId,
  OverlayPosition,
  OverlayPreset,
} from "@/lib/overlays";

function positionClasses(pos: OverlayPosition, variant: "poster" | "wide"): string {
  const bottom = variant === "wide" ? "bottom-6" : "bottom-2";
  switch (pos) {
    case "top-left":
      return "top-2 left-2 items-start";
    case "top-right":
      return "top-2 right-2 items-end";
    case "bottom-left":
      return `${bottom} left-2 items-start`;
    case "bottom-right":
      return `${bottom} right-2 items-end`;
  }
}

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

export default function CardOverlays({ data, prefs, variant = "poster" }: CardOverlaysProps) {
  const preset = getPreset(prefs.preset);
  const groups = new Map<OverlayPosition, ResolvedBadge[]>();

  for (const def of OVERLAY_REGISTRY) {
    const config = prefs.items[def.id];
    if (!config?.enabled) continue;
    if (isOverlaySuppressed(def.id, prefs)) continue;
    const badge = resolveBadge(def, data, preset, config.accentColor, config.showIcon);
    if (!badge) continue;
    const list = groups.get(config.position) ?? [];
    list.push(badge);
    groups.set(config.position, list);
  }

  if (groups.size === 0) return null;

  return (
    <>
      {OVERLAY_POSITIONS.map((pos) => {
        const badges = groups.get(pos);
        if (!badges?.length) return null;

        return (
          <div
            key={pos}
            className={`pointer-events-none absolute z-10 flex flex-col ${preset.gapClass} ${positionClasses(pos, variant)}`}
          >
            {badges.map((badge) => (
              <span
                key={badge.def.id}
                className={`inline-flex items-center gap-1 ${preset.badgeClass}`}
                style={preset.badgeStyle(badge.accentColor)}
              >
                {badge.iconId && <OverlayIcon iconId={badge.iconId} size={preset.iconSize} />}
                {!badge.def.iconOnly && badge.label}
              </span>
            ))}
          </div>
        );
      })}
    </>
  );
}
