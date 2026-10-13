/** Reject values that could break out of a CSS declaration. */
const UNSAFE_CSS_VALUE = /[;{}]|\/\*|<\//;

/** Cinema Dark's label color on primary surfaces (--primary-foreground). */
const DARK_LABEL = "#141417";
const WHITE_LABEL = "#ffffff";

/**
 * Returns the stored accent as lowercase "#rrggbb" when it is a "#rgb" or
 * "#rrggbb" hex color, else null. The setting is free text, so anything else
 * gets no derived label color.
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

/**
 * The CSS custom properties the admin's accent sets, or null when the stored
 * value is unsafe to write into a declaration. The accent recolors the primary
 * action color, the focus ring, and the sidebar's primary accent. When white
 * reads better on it, the labels drawn on those surfaces turn white too; a
 * light accent keeps Cinema Dark's own dark labels.
 */
export function accentDeclarations(accent: string): Record<string, string> | null {
  if (UNSAFE_CSS_VALUE.test(accent)) return null;
  const declarations: Record<string, string> = {
    primary: accent,
    ring: accent,
    "sidebar-primary": accent,
  };
  const hex = normalizeHex(accent);
  if (hex && prefersWhiteLabel(hex)) {
    declarations["primary-foreground"] = WHITE_LABEL;
    declarations["sidebar-primary-foreground"] = WHITE_LABEL;
  }
  return declarations;
}
