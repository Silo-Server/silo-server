// Reader content palettes. Each theme is a light/dark pair; AMOLED is
// dark-only (its light slot aliases the dark palette so the variant toggle
// can be disabled without a special data shape).

export type ReaderThemeVariant = "light" | "dark";

export type ReaderThemeName =
  | "default"
  | "sepia"
  | "gray"
  | "dawnlight"
  | "ember"
  | "aurora"
  | "ocean"
  | "meadow"
  | "rosewood"
  | "amoled";

export type ReaderPalette = {
  background: string;
  foreground: string;
  link: string;
};

type ReaderThemeDefinition = {
  label: string;
  darkOnly?: true;
  light: ReaderPalette;
  dark: ReaderPalette;
};

const AMOLED_PALETTE: ReaderPalette = {
  background: "#000000",
  foreground: "#c9c9c9",
  link: "#7aa2f7",
};

export const READER_THEMES: Record<ReaderThemeName, ReaderThemeDefinition> = {
  default: {
    label: "Default",
    light: { background: "#ffffff", foreground: "#171717", link: "#2563eb" },
    dark: { background: "#111827", foreground: "#f8fafc", link: "#93c5fd" },
  },
  sepia: {
    label: "Sepia",
    light: { background: "#f4ecd8", foreground: "#2f261b", link: "#8b5a2b" },
    dark: { background: "#211b12", foreground: "#c8b697", link: "#c99a5b" },
  },
  gray: {
    label: "Gray",
    light: { background: "#eceef0", foreground: "#33383d", link: "#4a6fa5" },
    dark: { background: "#23262a", foreground: "#b9bfc6", link: "#8ab0dd" },
  },
  dawnlight: {
    label: "Dawnlight",
    light: { background: "#fdf6e3", foreground: "#586e75", link: "#268bd2" },
    dark: { background: "#002b36", foreground: "#93a1a1", link: "#268bd2" },
  },
  ember: {
    label: "Ember",
    light: { background: "#fbf1c7", foreground: "#3c3836", link: "#af3a03" },
    dark: { background: "#282828", foreground: "#ebdbb2", link: "#fe8019" },
  },
  aurora: {
    label: "Aurora",
    light: { background: "#eceff4", foreground: "#2e3440", link: "#5e81ac" },
    dark: { background: "#2e3440", foreground: "#d8dee9", link: "#88c0d0" },
  },
  ocean: {
    label: "Ocean",
    light: { background: "#e8f0f7", foreground: "#1e3a52", link: "#2471a3" },
    dark: { background: "#0d1b2a", foreground: "#a9c1d9", link: "#64a8dd" },
  },
  meadow: {
    label: "Meadow",
    light: { background: "#eef3e7", foreground: "#2f3b2a", link: "#3f7d44" },
    dark: { background: "#1a2318", foreground: "#b8c9a8", link: "#7fb069" },
  },
  rosewood: {
    label: "Rosewood",
    light: { background: "#f7edee", foreground: "#4a2f33", link: "#a4494f" },
    dark: { background: "#241a1c", foreground: "#d3b8bc", link: "#d98a90" },
  },
  amoled: {
    label: "AMOLED",
    darkOnly: true,
    light: AMOLED_PALETTE,
    dark: AMOLED_PALETTE,
  },
};

export function readerPalette(
  name: ReaderThemeName,
  variant: ReaderThemeVariant,
): ReaderPalette & { scheme: ReaderThemeVariant } {
  const theme = READER_THEMES[name] ?? READER_THEMES.default;
  const effective: ReaderThemeVariant = theme.darkOnly ? "dark" : variant;
  return { ...theme[effective], scheme: effective };
}

export function legacyThemeFor(
  name: ReaderThemeName,
  variant: ReaderThemeVariant,
): "light" | "sepia" | "dark" {
  if (readerPalette(name, variant).scheme === "dark") return "dark";
  if (name === "sepia") return "sepia";
  return "light";
}

export function themeFromLegacy(theme: string): {
  themeName: ReaderThemeName;
  themeVariant: ReaderThemeVariant;
} {
  switch (theme) {
    case "sepia":
      return { themeName: "sepia", themeVariant: "light" };
    case "dark":
      return { themeName: "default", themeVariant: "dark" };
    default:
      return { themeName: "default", themeVariant: "light" };
  }
}
