import type { OverlayPreset, PresetId } from "./types";

// Presets are pure data — no runtime branching, no per-render computation.
// badgeStyle() is called with the resolved accent color and returns the
// inline style applied to the badge container. Tailwind classes own
// typography and shape; inline styles own colors so dynamic accent values
// don't fight Tailwind's purge step.

export const OVERLAY_PRESETS: Record<PresetId, OverlayPreset> = {
  minimal: {
    id: "minimal",
    label: "Minimal",
    description: "Near-invisible. Tiny text, no background.",
    badgeClass:
      "rounded-sm px-1 py-0 text-[9px] font-semibold tracking-widest uppercase leading-none",
    badgeStyle: (accent) => ({
      background: "transparent",
      color: accent ?? "rgba(255,255,255,0.85)",
      textShadow: "0 1px 2px rgba(0,0,0,0.85)",
    }),
    iconSize: 10,
    preferIcon: false,
    gapClass: "gap-0.5",
    accentStrategy: "text",
  },
  classic: {
    id: "classic",
    label: "Classic",
    description: "Semi-transparent dark pill with a white border. The default.",
    badgeClass:
      "rounded-full border border-white/15 px-2 py-0.5 text-[10px] font-semibold tracking-wide uppercase leading-none",
    badgeStyle: (accent) => ({
      background: accent ? `color-mix(in srgb, ${accent} 28%, rgba(0,0,0,0.6))` : "rgba(0,0,0,0.6)",
      color: "white",
    }),
    iconSize: 11,
    preferIcon: false,
    gapClass: "gap-1",
    accentStrategy: "bg",
  },
  vibrant: {
    id: "vibrant",
    label: "Vibrant",
    description: "Opaque, accent-colored badges. High contrast.",
    badgeClass:
      "rounded-md px-2 py-0.5 text-[10px] font-bold tracking-wide uppercase leading-none shadow-sm",
    badgeStyle: (accent) => ({
      background: accent ?? "rgba(220,220,220,0.95)",
      color: accent ? "white" : "black",
    }),
    iconSize: 12,
    preferIcon: true,
    gapClass: "gap-1",
    accentStrategy: "bg",
  },
  pill: {
    id: "pill",
    label: "Pill",
    description: "Larger pill with more padding. Works well with icons.",
    badgeClass:
      "rounded-full border border-white/15 px-2.5 py-1 text-[10px] font-semibold tracking-wide uppercase leading-none",
    badgeStyle: (accent) => ({
      background: accent
        ? `color-mix(in srgb, ${accent} 20%, rgba(20,20,30,0.7))`
        : "rgba(20,20,30,0.7)",
      color: "white",
    }),
    iconSize: 12,
    preferIcon: true,
    gapClass: "gap-1",
    accentStrategy: "bg",
  },
  square: {
    id: "square",
    label: "Square",
    description: "Blocky, high-density. Plex-inspired.",
    badgeClass:
      "rounded-sm px-1.5 py-0.5 text-[9px] font-bold tracking-widest uppercase leading-none",
    badgeStyle: (accent) => ({
      background: "rgba(0,0,0,0.8)",
      color: accent ?? "white",
      borderLeft: accent ? `2px solid ${accent}` : undefined,
    }),
    iconSize: 10,
    preferIcon: false,
    gapClass: "gap-0.5",
    accentStrategy: "border",
  },
};

export const PRESET_IDS = [
  "minimal",
  "classic",
  "vibrant",
  "pill",
  "square",
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
