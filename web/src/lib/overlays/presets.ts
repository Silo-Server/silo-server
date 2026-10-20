import type { OverlayPreset, PresetId } from "./types";

// Presets are pure data — no runtime branching, no per-render computation.
// badgeStyle() is called with the resolved accent color and returns the
// inline style applied to the badge container. CardOverlays overrides the
// fixed Tailwind geometry with card-relative values; those classes remain as
// the settings-picker rendering and as a fallback without container units.

export const OVERLAY_PRESETS: Record<PresetId, OverlayPreset> = {
  minimal: {
    id: "minimal",
    label: "Minimal",
    description: "Near-invisible. Tiny text, no background.",
    layout: "stack",
    sourceChips: false,
    fontWeight: 600,
    uppercase: true,
    badgeClass:
      "rounded-sm px-1 py-0 text-[0.5625rem] font-semibold tracking-widest uppercase leading-none",
    badgeStyle: (accent) => ({
      background: "transparent",
      color: accent ?? "rgba(255,255,255,0.85)",
      textShadow: "0 1px 2px rgba(0,0,0,0.85)",
    }),
    fontSize: 9,
    paddingInline: 4,
    paddingBlock: 0,
    borderRadius: 8,
    borderRadiusVariable: "--radius-sm",
    textShadow: { x: 0, y: 1, blur: 2, color: "rgba(0,0,0,0.85)" },
    iconSize: 10,
    iconGap: 4,
    preferIcon: false,
    gapClass: "gap-0.5",
    stackGap: 2,
    accentStrategy: "text",
  },
  classic: {
    id: "classic",
    label: "Classic",
    description: "Semi-transparent dark pill with a white border. The default.",
    layout: "stack",
    sourceChips: false,
    fontWeight: 600,
    uppercase: true,
    badgeClass:
      "rounded-full border border-white/15 px-2 py-0.5 text-[0.625rem] font-semibold tracking-wide uppercase leading-none",
    badgeStyle: (accent) => ({
      background: accent ? `color-mix(in srgb, ${accent} 28%, rgba(0,0,0,0.6))` : "rgba(0,0,0,0.6)",
      color: "white",
    }),
    fontSize: 10,
    paddingInline: 8,
    paddingBlock: 2,
    borderRadius: "full",
    borderWidth: 1,
    iconSize: 11,
    iconGap: 4,
    preferIcon: false,
    gapClass: "gap-1",
    stackGap: 4,
    accentStrategy: "bg",
  },
  vibrant: {
    id: "vibrant",
    label: "Vibrant",
    description: "Opaque, accent-colored badges. High contrast.",
    layout: "stack",
    sourceChips: false,
    fontWeight: 700,
    uppercase: true,
    badgeClass:
      "rounded-md px-2 py-0.5 text-[0.625rem] font-bold tracking-wide uppercase leading-none shadow-sm",
    badgeStyle: (accent) => ({
      background: accent ?? "rgba(220,220,220,0.95)",
      color: accent ? "white" : "black",
    }),
    fontSize: 10,
    paddingInline: 8,
    paddingBlock: 2,
    borderRadius: 10,
    borderRadiusVariable: "--radius-md",
    boxShadow: { x: 0, y: 1, blur: 2, spread: 0, color: "rgb(0 0 0 / 0.25)" },
    iconSize: 12,
    iconGap: 4,
    preferIcon: true,
    gapClass: "gap-1",
    stackGap: 4,
    accentStrategy: "bg",
  },
  pill: {
    id: "pill",
    label: "Pill",
    description: "Larger pill with more padding. Works well with icons.",
    layout: "stack",
    sourceChips: false,
    fontWeight: 600,
    uppercase: true,
    badgeClass:
      "rounded-full border border-white/15 px-2.5 py-1 text-[0.625rem] font-semibold tracking-wide uppercase leading-none",
    badgeStyle: (accent) => ({
      background: accent
        ? `color-mix(in srgb, ${accent} 20%, rgba(20,20,30,0.7))`
        : "rgba(20,20,30,0.7)",
      color: "white",
    }),
    fontSize: 10,
    paddingInline: 10,
    paddingBlock: 4,
    borderRadius: "full",
    borderWidth: 1,
    iconSize: 12,
    iconGap: 4,
    preferIcon: true,
    gapClass: "gap-1",
    stackGap: 4,
    accentStrategy: "bg",
  },
  square: {
    id: "square",
    label: "Square",
    description: "Blocky, high-density. Plex-inspired.",
    layout: "stack",
    sourceChips: false,
    fontWeight: 700,
    uppercase: true,
    badgeClass:
      "rounded-sm px-1.5 py-0.5 text-[0.5625rem] font-bold tracking-widest uppercase leading-none",
    badgeStyle: (accent) => ({
      background: "rgba(0,0,0,0.8)",
      color: accent ?? "white",
      borderLeftColor: accent,
      borderLeftStyle: accent ? "solid" : undefined,
      borderLeftWidth: accent ? "2px" : undefined,
    }),
    fontSize: 9,
    paddingInline: 6,
    paddingBlock: 2,
    borderRadius: 8,
    borderRadiusVariable: "--radius-sm",
    borderLeftWidth: 2,
    iconSize: 10,
    iconGap: 4,
    preferIcon: false,
    gapClass: "gap-0.5",
    stackGap: 2,
    accentStrategy: "border",
  },
  plate: {
    id: "plate",
    label: "Plate",
    description: "Large dark plates with heavy type. Ratings lead with a coloured source chip.",
    layout: "stack",
    sourceChips: true,
    fontWeight: 800,
    uppercase: true,
    badgeClass:
      "rounded-md border border-white/10 px-2.5 py-1 text-xs font-extrabold tracking-wide uppercase leading-none",
    badgeStyle: (accent) => ({
      background: "rgba(0,0,0,0.72)",
      color: "white",
      borderColor: accent ? `color-mix(in srgb, ${accent} 45%, transparent)` : undefined,
    }),
    fontSize: 12,
    paddingInline: 7,
    paddingBlock: 4,
    borderRadius: 6,
    borderWidth: 1,
    boxShadow: { x: 0, y: 2, blur: 6, spread: 0, color: "rgb(0 0 0 / 0.35)" },
    iconSize: 13,
    iconGap: 5,
    preferIcon: true,
    gapClass: "gap-1",
    stackGap: 4,
    accentStrategy: "border",
  },
  band: {
    id: "band",
    label: "Edge band",
    description: "Each edge's badges in one strip of marks over a soft scrim.",
    layout: "band",
    sourceChips: false,
    fontWeight: 800,
    uppercase: true,
    badgeClass: "px-0 py-0 text-xs font-extrabold tracking-wide uppercase leading-none",
    badgeStyle: (accent) => ({
      background: "transparent",
      color: accent ?? "white",
      textShadow: "0 1px 3px rgba(0,0,0,0.75)",
    }),
    fontSize: 12.5,
    paddingInline: 0,
    paddingBlock: 1,
    borderRadius: 0,
    textShadow: { x: 0, y: 1, blur: 3, color: "rgba(0,0,0,0.75)" },
    iconSize: 13,
    iconGap: 3,
    preferIcon: false,
    gapClass: "gap-1",
    stackGap: 7,
    accentStrategy: "text",
  },
};

export const PRESET_IDS = [
  "minimal",
  "classic",
  "vibrant",
  "pill",
  "square",
  "plate",
  "band",
] as const satisfies readonly PresetId[];

export function getPreset(id: PresetId): OverlayPreset {
  return OVERLAY_PRESETS[id] ?? OVERLAY_PRESETS.classic;
}

// Curated accent color palette shown in the settings UI for per-overlay color
// overrides. Values cover most reasonable contrast scenarios over a dark badge
// background.
export const ACCENT_PALETTE: { label: string; value: string }[] = [
  { label: "Gold", value: "#f5c518" }, // IMDb yellow
  { label: "Tomato", value: "#fa320a" }, // RT critic red
  { label: "Orange", value: "#f97316" },
  { label: "Amber", value: "#f59e0b" },
  { label: "Emerald", value: "#10b981" },
  { label: "Cyan", value: "#06b6d4" },
  { label: "Blue", value: "#3b82f6" },
  { label: "Indigo", value: "#6366f1" },
  { label: "Violet", value: "#8b5cf6" },
  { label: "Pink", value: "#ec4899" },
  { label: "Slate", value: "#64748b" },
  { label: "White", value: "#ffffff" },
];
