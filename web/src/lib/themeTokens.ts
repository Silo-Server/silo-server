/** All CSS custom property tokens that a theme can override. */

export type ThemeToken =
  | "background"
  | "foreground"
  | "card"
  | "card-foreground"
  | "popover"
  | "popover-foreground"
  | "surface"
  | "surface-hover"
  | "surface-raised"
  | "primary"
  | "primary-foreground"
  | "secondary"
  | "secondary-foreground"
  | "muted"
  | "muted-foreground"
  | "accent"
  | "accent-foreground"
  | "destructive"
  | "destructive-foreground"
  | "border"
  | "input"
  | "ring"
  | "sidebar"
  | "sidebar-foreground"
  | "sidebar-primary"
  | "sidebar-primary-foreground"
  | "sidebar-accent"
  | "sidebar-accent-foreground"
  | "sidebar-border"
  | "sidebar-section-divider"
  | "sidebar-ring"
  | "ambient"
  | "radius"
  | "font-body";

export type TokenGroup =
  | "Surfaces"
  | "Interactive"
  | "Sidebar"
  | "Borders & Focus"
  | "Shape & Font";

export type TokenInputType = "color" | "radius" | "font";

export interface TokenMeta {
  token: ThemeToken;
  label: string;
  group: TokenGroup;
  inputType: TokenInputType;
}

export const THEME_TOKENS: TokenMeta[] = [
  // Surfaces
  { token: "background", label: "Background", group: "Surfaces", inputType: "color" },
  { token: "foreground", label: "Foreground", group: "Surfaces", inputType: "color" },
  { token: "card", label: "Card", group: "Surfaces", inputType: "color" },
  { token: "card-foreground", label: "Card Text", group: "Surfaces", inputType: "color" },
  { token: "popover", label: "Popover", group: "Surfaces", inputType: "color" },
  { token: "popover-foreground", label: "Popover Text", group: "Surfaces", inputType: "color" },
  { token: "surface", label: "Surface", group: "Surfaces", inputType: "color" },
  { token: "surface-hover", label: "Surface Hover", group: "Surfaces", inputType: "color" },
  { token: "surface-raised", label: "Surface Raised", group: "Surfaces", inputType: "color" },

  // Interactive
  { token: "primary", label: "Primary", group: "Interactive", inputType: "color" },
  { token: "primary-foreground", label: "Primary Text", group: "Interactive", inputType: "color" },
  { token: "secondary", label: "Secondary", group: "Interactive", inputType: "color" },
  {
    token: "secondary-foreground",
    label: "Secondary Text",
    group: "Interactive",
    inputType: "color",
  },
  { token: "muted", label: "Muted", group: "Interactive", inputType: "color" },
  { token: "muted-foreground", label: "Muted Text", group: "Interactive", inputType: "color" },
  { token: "accent", label: "Accent", group: "Interactive", inputType: "color" },
  { token: "accent-foreground", label: "Accent Text", group: "Interactive", inputType: "color" },
  { token: "destructive", label: "Destructive", group: "Interactive", inputType: "color" },
  {
    token: "destructive-foreground",
    label: "Destructive Text",
    group: "Interactive",
    inputType: "color",
  },
  { token: "ambient", label: "Ambient Glow", group: "Interactive", inputType: "color" },

  // Sidebar
  { token: "sidebar", label: "Sidebar", group: "Sidebar", inputType: "color" },
  { token: "sidebar-foreground", label: "Sidebar Text", group: "Sidebar", inputType: "color" },
  { token: "sidebar-primary", label: "Sidebar Primary", group: "Sidebar", inputType: "color" },
  {
    token: "sidebar-primary-foreground",
    label: "Sidebar Primary Text",
    group: "Sidebar",
    inputType: "color",
  },
  { token: "sidebar-accent", label: "Sidebar Accent", group: "Sidebar", inputType: "color" },
  {
    token: "sidebar-accent-foreground",
    label: "Sidebar Accent Text",
    group: "Sidebar",
    inputType: "color",
  },
  { token: "sidebar-border", label: "Sidebar Border", group: "Sidebar", inputType: "color" },
  {
    token: "sidebar-section-divider",
    label: "Sidebar Section Divider",
    group: "Sidebar",
    inputType: "color",
  },
  { token: "sidebar-ring", label: "Sidebar Ring", group: "Sidebar", inputType: "color" },

  // Borders & Focus
  { token: "border", label: "Border", group: "Borders & Focus", inputType: "color" },
  { token: "input", label: "Input Border", group: "Borders & Focus", inputType: "color" },
  { token: "ring", label: "Focus Ring", group: "Borders & Focus", inputType: "color" },

  // Shape & Font
  { token: "radius", label: "Border Radius", group: "Shape & Font", inputType: "radius" },
  { token: "font-body", label: "Font Family", group: "Shape & Font", inputType: "font" },
];

/** Tokens grouped by category for the editor UI. */
export const TOKEN_GROUPS: Record<TokenGroup, TokenMeta[]> = THEME_TOKENS.reduce(
  (acc, meta) => {
    (acc[meta.group] ??= []).push(meta);
    return acc;
  },
  {} as Record<TokenGroup, TokenMeta[]>,
);

/** Token group display order. */
export const TOKEN_GROUP_ORDER: TokenGroup[] = [
  "Surfaces",
  "Interactive",
  "Sidebar",
  "Borders & Focus",
  "Shape & Font",
];

/** Available font families, self-hosted by fonts.css. */
export const AVAILABLE_FONTS = ["Outfit", "Sora", "Urbanist", "Manrope"];

/** Read the current computed value of a CSS custom property from the DOM. */
export function getComputedToken(token: ThemeToken): string {
  return getComputedStyle(document.documentElement).getPropertyValue(`--${token}`).trim();
}
