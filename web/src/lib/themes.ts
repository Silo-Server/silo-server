export const THEME_IDS = [
  "midnight-cinema",
  "cinema-light",
  "cobalt-studio",
  "oxblood-noir",
  "evergreen-studio",
] as const;

export type ThemeId = (typeof THEME_IDS)[number];

export interface ThemeDefinition {
  id: ThemeId;
  label: string;
  fontFamily: string;
  /** Whether the theme paints a light or a dark surface. Drives theme-aware
   * assets (logos) and third-party widget themes (toasts). */
  appearance: "light" | "dark";
  /** Accent/primary color shown in the theme picker preview */
  previewAccent: string;
  /** Background color shown in the theme picker preview */
  previewBg: string;
  /** Short description of the theme's aesthetic */
  description?: string;
  /** Whether this theme should appear in the curated picker */
  curated?: boolean;
}

export const THEMES: Record<ThemeId, ThemeDefinition> = {
  "midnight-cinema": {
    id: "midnight-cinema",
    label: "Cinema Dark",
    fontFamily: "Outfit",
    appearance: "dark",
    previewAccent: "#e8e8ec",
    previewBg: "#141417",
    description: "Monochromatic cinema — content is the color",
    curated: true,
  },
  "cinema-light": {
    id: "cinema-light",
    label: "Cinema Light",
    fontFamily: "Outfit",
    appearance: "light",
    previewAccent: "#1a1a1e",
    previewBg: "#f4f4f6",
    description: "Light monochromatic cinema — content is the color",
    curated: true,
  },
  "cobalt-studio": {
    id: "cobalt-studio",
    label: "Cobalt",
    fontFamily: "Outfit",
    appearance: "dark",
    previewAccent: "#78aefc",
    previewBg: "#101722",
    description: "Cool blue graphite with crisp contrast",
    curated: true,
  },
  "oxblood-noir": {
    id: "oxblood-noir",
    label: "Oxblood",
    fontFamily: "Outfit",
    appearance: "dark",
    previewAccent: "#d16a78",
    previewBg: "#171113",
    description: "Deep red-black with restrained luxury warmth",
    curated: true,
  },
  "evergreen-studio": {
    id: "evergreen-studio",
    label: "Evergreen",
    fontFamily: "Outfit",
    appearance: "dark",
    previewAccent: "#5bc39d",
    previewBg: "#101715",
    description: "Refined evergreen accents on dense graphite",
    curated: true,
  },
};

export const DEFAULT_THEME: ThemeId = "midnight-cinema";

export const CURATED_THEME_IDS = THEME_IDS.filter((id) => THEMES[id].curated);
