/** Cinema Dark's label color on primary surfaces (--primary-foreground). */
const DARK_LABEL = "#141417";
const WHITE_LABEL = "#ffffff";
/** White falls short of 4.5:1 only above relative luminance 0.183, where black reads at 4.66:1 or more. */
const BLACK_LABEL = "#000000";
/** WCAG AA for normal text: the labels on primary surfaces are short body text. */
const MIN_LABEL_CONTRAST = 4.5;

/**
 * Returns the stored accent as lowercase "#rrggbb" when it is a "#rgb" or
 * "#rrggbb" hex color, else null. The setting is free text, but the admin
 * picker only writes hex, and email buttons (internal/mail) honor only hex too.
 */
function normalizeHex(color: string): string | null {
  const match = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(color.trim());
  if (!match?.[1]) return null;
  const digits = match[1].toLowerCase();
  return digits.length === 3
    ? `#${[...digits].map((digit) => digit + digit).join("")}`
    : `#${digits}`;
}

function relativeLuminance(hex: string): number {
  const value = parseInt(hex.slice(1), 16);
  const channel = (shift: number) => {
    const c = ((value >> shift) & 0xff) / 255;
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(16) + 0.7152 * channel(8) + 0.0722 * channel(0);
}

/**
 * Whether white text has higher WCAG contrast than Cinema Dark's dark label on
 * the accent. Email buttons make the same choice (internal/mail).
 */
function prefersWhiteLabel(hex: string): boolean {
  const background = relativeLuminance(hex);
  const dark = relativeLuminance(DARK_LABEL);
  return (1 + 0.05) / (background + 0.05) > (background + 0.05) / (dark + 0.05);
}

/** The video player's panel color: the darkest surface its accent marks. */
const PLAYER_PANEL = "#101013";
/** The player uses the accent for short labels as well as marks, so text contrast. */
const MIN_PLAYER_CONTRAST = 4.5;

function contrastRatio(a: string, b: string): number {
  const [light, dark] = [relativeLuminance(a), relativeLuminance(b)].sort((x, y) => y - x);
  return (light! + 0.05) / (dark! + 0.05);
}

/** Moves `hex` toward `target` by `amount` (0 keeps `hex`, 1 gives `target`). */
function mixToward(hex: string, target: string, amount: number): string {
  const from = parseInt(hex.slice(1), 16);
  const to = parseInt(target.slice(1), 16);
  const channel = (shift: number) => {
    const a = (from >> shift) & 0xff;
    const b = (to >> shift) & 0xff;
    return Math.round(a + (b - a) * amount)
      .toString(16)
      .padStart(2, "0");
  };
  return `#${channel(16)}${channel(8)}${channel(0)}`;
}

/** The accent mixed toward white in twentieths until `reads` accepts it, or white. */
function lightenUntil(hex: string, reads: (candidate: string) => boolean): string {
  for (let step = 0; step <= 20; step++) {
    const candidate = mixToward(hex, WHITE_LABEL, step / 20);
    if (reads(candidate)) return candidate;
  }
  return WHITE_LABEL;
}

/** The label drawn on an accent: white or Cinema Dark's dark label, whichever reads better. */
function labelOn(hex: string): string {
  return prefersWhiteLabel(hex) ? WHITE_LABEL : DARK_LABEL;
}

/**
 * The label on the accent's primary surfaces (buttons, pills): white or the
 * dark label, whichever reads better, or black when neither reaches 4.5:1, so
 * the accent itself is drawn as the admin stored it. Email buttons make the
 * same choice (internal/mail).
 */
function primaryLabelOn(hex: string): string {
  const label = labelOn(hex);
  return contrastRatio(hex, label) >= MIN_LABEL_CONTRAST ? label : BLACK_LABEL;
}

/**
 * The accent as the video player draws it on its dark panels: unchanged when
 * it already reads there, otherwise lightened just enough that a selected
 * row's check and the focus ring stay visible, and the label on it (the speed
 * badge) still reads.
 */
function accentOnDark(hex: string): string {
  return lightenUntil(
    hex,
    (candidate) =>
      contrastRatio(candidate, PLAYER_PANEL) >= MIN_PLAYER_CONTRAST &&
      contrastRatio(candidate, labelOn(candidate)) >= MIN_PLAYER_CONTRAST,
  );
}

/** Cinema Dark's lightest surface under text, --surface-raised. */
const LIGHTEST_SURFACE = "#28282e";
/** High contrast mixes 20% of its white boost into --surface-raised (app.css). */
const LIGHTEST_SURFACE_HIGH_CONTRAST = mixToward(LIGHTEST_SURFACE, WHITE_LABEL, 0.2);
/** The strongest accent tint text sits on: `bg-primary/20`. */
const STRONGEST_TINT = 0.2;
/** WCAG AA for normal text. */
const MIN_TEXT_CONTRAST = 4.5;

/**
 * The accent as text (`text-primary`) on Cinema Dark: unchanged when it
 * already reads at 4.5:1 on `surface`, the lightest surface text sits on, and
 * on the accent's own strongest tint over it; otherwise lightened just enough.
 * Darker surfaces then read at least as well.
 */
function accentText(hex: string, surface: string): string {
  const tint = mixToward(surface, hex, STRONGEST_TINT);
  return lightenUntil(
    hex,
    (candidate) =>
      contrastRatio(candidate, surface) >= MIN_TEXT_CONTRAST &&
      contrastRatio(candidate, tint) >= MIN_TEXT_CONTRAST,
  );
}

/** Cinema Dark's page (--background), which the theme's --primary-hover fades the primary toward. */
const PAGE = "#141417";

/**
 * The accent's hover fill (--primary-hover): faded a tenth toward the page,
 * like the theme's, unless that takes `label` below 4.5:1. Only a dark label
 * can fall below 4.5:1 on that fade, so the accent then lightens a tenth instead,
 * which can only raise it. The accent always sets the color it checked: the
 * theme's color-mix keeps fractional channels and can land just under it.
 */
function hoverFill(hex: string, label: string): string {
  const faded = mixToward(hex, PAGE, 0.1);
  return contrastRatio(faded, label) >= MIN_LABEL_CONTRAST
    ? faded
    : mixToward(hex, WHITE_LABEL, 0.1);
}

/**
 * The CSS custom properties the admin's accent sets, or null when the stored
 * value is not a hex color, which is then ignored as email ignores it. The
 * accent recolors the primary action color, the focus ring, and the sidebar's
 * primary accent. The labels drawn on those surfaces turn white when white
 * reads better on it, or black on a mid-tone accent where neither white nor
 * Cinema Dark's dark label reaches 4.5:1; otherwise they keep the theme's own.
 */
export function accentDeclarations(accent: string): Record<string, string> | null {
  const hex = normalizeHex(accent);
  if (!hex) return null;
  const onDark = accentOnDark(hex);
  const declarations: Record<string, string> = {
    primary: hex,
    ring: hex,
    "sidebar-primary": hex,
    "accent-on-dark": onDark,
    "accent-on-dark-foreground": labelOn(onDark),
    // app.css maps text-primary to --primary-text; BrandingProvider swaps in
    // the high contrast value under that setting.
    "primary-text": accentText(hex, LIGHTEST_SURFACE),
    "primary-text-high-contrast": accentText(hex, LIGHTEST_SURFACE_HIGH_CONTRAST),
  };
  const label = primaryLabelOn(hex);
  if (label !== DARK_LABEL) {
    declarations["primary-foreground"] = label;
    declarations["sidebar-primary-foreground"] = label;
  }
  declarations["primary-hover"] = hoverFill(hex, label);
  return declarations;
}
