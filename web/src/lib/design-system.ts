/**
 * ═══════════════════════════════════════════════════════════════════
 * SILO DESIGN SYSTEM
 * A cinema-first design language for media streaming interfaces
 * ═══════════════════════════════════════════════════════════════════
 *
 * Human-facing design guidance lives in `web/docs/design-system.md`.
 * This file contains the code-backed constants and shared patterns
 * that the web app imports.
 *
 * Related files:
 *   docs/design-system.md — Design philosophy and usage guidance
 *   src/app.css          — CSS tokens (colors, radii, shadows, motion)
 *   src/lib/themes.ts    — Theme definitions and metadata
 *
 * ─── HOW TO USE ───
 *
 * 1. COLORS: Always use semantic CSS variables, never raw hex.
 *      ✅  className="bg-background text-foreground"
 *      ✅  className="bg-surface text-muted-foreground"
 *      ❌  className="bg-[#1a1a1f]"
 *
 * 2. SPACING: Use Tailwind spacing scale for consistency.
 *      Layout constants below define semantic spacing.
 *
 * 3. TYPOGRAPHY: Use the type scale constants. Hero/display text
 *      uses --font-display; everything else uses --font-body.
 *
 * 4. MOTION: Use duration/easing CSS variables for transitions.
 *      className="transition-all duration-(--duration-normal)"
 *
 * 5. PATTERNS: Use the utility classes defined in app.css.
 *      .hero-gradient, .glass, .glass-subtle, .media-card, etc.
 */

// ────────────────────────────────────────────────────────────────
// § 1  DESIGN PRINCIPLES
// ────────────────────────────────────────────────────────────────

export const DESIGN_PRINCIPLES = [
  {
    id: "content-first",
    name: "Content is King",
    description:
      "Media artwork and imagery are the primary visual elements. UI chrome " +
      "recedes to let content shine. Poster art, backdrops, and thumbnails " +
      "provide the visual richness — not the UI itself.",
    examples: [
      "Hero sections use full-bleed backdrop images",
      "Card grids let poster art dominate; text labels are secondary",
      "Navigation and controls use low-contrast, minimal styling",
    ],
  },
  {
    id: "cinematic-darkness",
    name: "Cinematic Darkness",
    description:
      "Deep, dark surfaces create a theater-like atmosphere. Multiple " +
      "layers of darkness (background → surface → raised) establish depth " +
      "without harsh contrast. The darkness makes media imagery pop.",
    examples: [
      "Page backgrounds are near-black (#121215 – #1e1e2e range)",
      "Sidebars are darker than the main background",
      "Cards use a subtle surface lift, not dramatic contrast",
    ],
  },
  {
    id: "subtle-chrome",
    name: "Subtle Chrome",
    description:
      "Borders, dividers, and backgrounds are minimal and low-contrast. " +
      "Interactive elements use opacity shifts and soft background " +
      "changes rather than bold outlines or shadows.",
    examples: [
      "Borders are 1px with ~8% opacity difference from surface",
      "Hover states change background, not border or shadow",
      "Active states use a subtle background highlight + text color change",
    ],
  },
  {
    id: "clear-hierarchy",
    name: "Clear Hierarchy",
    description:
      "Information importance is conveyed through size, weight, and " +
      "opacity — not color. Titles are large and bold. Metadata is small " +
      "and muted. Color is reserved for accent cues and actions.",
    examples: [
      "Hero title: text-5xl font-bold (largest, heaviest)",
      "Section headers: text-xl font-semibold",
      "Metadata (year, rating): text-xs text-muted-foreground",
      "Primary actions (Play): primary color, largest interactive element",
    ],
  },
  {
    id: "ambient-immersion",
    name: "Ambient Immersion",
    description:
      "The UI should feel alive with the content it displays. Featured " +
      "artwork influences the surrounding atmosphere through subtle color " +
      "bleed and ambient glow. The page should feel like it belongs to the " +
      "content currently on screen.",
    examples: [
      "Hero backdrops emit a soft ambient glow via --ambient color",
      "Detail pages shift atmosphere to match the featured title",
      "Hero images use subtle Ken Burns motion (slow zoom/pan) to feel alive",
      "Hero carousel crossfades shift the ambient color smoothly",
    ],
  },
  {
    id: "discovery-first",
    name: "Discovery-First Layout",
    description:
      "Horizontal scroll rows are the primary discovery pattern. Users " +
      "browse by scanning rows of artwork, not by reading lists or " +
      "navigating grids. Grids are reserved for search results, 'view all' " +
      "pages, and library management.",
    examples: [
      "Home and discover pages use carousel rows as the primary layout",
      "Each row shows partially visible items at edges to signal scrollability",
      "Row items are large enough for artwork to read at a glance",
      "Section headers label each row; 'View All' leads to the full grid",
    ],
  },
  {
    id: "fluid-motion",
    name: "Fluid Motion",
    description:
      "Transitions are smooth and purposeful. Elements fade, slide, " +
      "and scale with ease. Motion guides attention without distraction. " +
      "Prefer CSS transitions over JS animation where possible. Ambient " +
      "cinematic motion uses longer durations and gentler easing than " +
      "interaction motion.",
    examples: [
      "Page transitions: fade + slight slide (300ms ease-out)",
      "Card hover: subtle scale(1.03) + brightness lift (150ms)",
      "Sidebar collapse: smooth width transition (250ms)",
      "Hero carousel: crossfade between items (500ms)",
      "Ken Burns: slow zoom scale(1)→scale(1.05), 12-18s linear",
      "Ambient color shift: --ambient transition, 1200ms gentle ease",
    ],
  },
  {
    id: "immersion-levels",
    name: "Immersion Levels",
    description:
      "The UI adapts its density and chrome based on how deeply a user is " +
      "engaged with content. Deeper engagement means less UI and more " +
      "content. Browse shows full navigation. Focus dims surroundings. " +
      "Detail gives content the full stage. Watch hides everything.",
    examples: [
      "Browse: sidebar visible, multiple rows, standard hero",
      "Focus: surrounding content dims, focused item expands",
      "Detail: full-bleed hero, sidebar recedes, page is about this content",
      "Watch: UI fully hidden, minimal transport on interaction",
    ],
  },
  {
    id: "responsive-density",
    name: "Responsive Density",
    description:
      "Information density adapts to the viewport. Desktop shows " +
      "persistent sidebar navigation and wide grids. Tablet reduces " +
      "columns. Mobile collapses to drawers and stacked layouts.",
    examples: [
      "Desktop: 5-6 column poster grid, persistent sidebar",
      "Tablet: 3-4 column grid, collapsible sidebar",
      "Mobile: 2 column grid, drawer navigation, stacked detail pages",
    ],
  },
] as const;

// ────────────────────────────────────────────────────────────────
// § 2  COLOR ARCHITECTURE
// ────────────────────────────────────────────────────────────────
//
// Colors are organized in semantic layers. Each theme provides
// values for these tokens. Never reference raw colors directly.
//
// ┌─────────────────────────────────────────────────────────────┐
// │  SURFACE HIERARCHY (darkest → lightest)                    │
// ├─────────────────────────────────────────────────────────────┤
// │  --sidebar           │ Navigation background (darkest)     │
// │  --background        │ Page background                     │
// │  --surface           │ Cards, panels, default surfaces     │
// │  --surface-hover     │ Surface on hover/focus              │
// │  --surface-raised    │ Elevated elements (modals, popovers)│
// │  --card              │ Card backgrounds (≈ surface)        │
// │  --popover           │ Dropdown/popover backgrounds        │
// ├─────────────────────────────────────────────────────────────┤
// │  TEXT HIERARCHY                                            │
// ├─────────────────────────────────────────────────────────────┤
// │  --foreground        │ Primary text (highest contrast)     │
// │  --card-foreground   │ Text on cards                       │
// │  --muted-foreground  │ Secondary/helper text               │
// ├─────────────────────────────────────────────────────────────┤
// │  INTERACTIVE                                               │
// ├─────────────────────────────────────────────────────────────┤
// │  --primary           │ Primary action color (buttons, CTAs)│
// │  --primary-foreground│ Text on primary elements            │
// │  --secondary         │ Secondary element backgrounds       │
// │  --accent            │ Active/highlighted backgrounds      │
// │  --destructive       │ Danger/delete actions               │
// │  --ring              │ Focus ring color                    │
// ├─────────────────────────────────────────────────────────────┤
// │  BORDERS                                                   │
// ├─────────────────────────────────────────────────────────────┤
// │  --border            │ Default border color                │
// │  --input             │ Input field border/background       │
// └─────────────────────────────────────────────────────────────┘

// ────────────────────────────────────────────────────────────────
// § 3  TYPOGRAPHY
// ────────────────────────────────────────────────────────────────

export const TYPOGRAPHY = {
  /**
   * Font stacks — defined per-theme via CSS variables.
   *
   * --font-body:    Primary UI font (nav, body text, labels)
   * --font-display: Hero titles, cinematic text (defaults to --font-body)
   * --font-mono:    Code blocks, technical data
   */
  families: {
    body: "var(--font-body)",
    display: "var(--font-display, var(--font-body))",
    mono: "'JetBrains Mono', 'Fira Code', ui-monospace, monospace",
  },

  /**
   * Type scale — use via Tailwind text-* classes.
   *
   * The scale follows a ~1.25 Major Third ratio. Each step
   * has a defined role in the UI hierarchy:
   */
  scale: {
    "2xs": { px: 10, rem: "0.625rem", use: "Badge labels, tiny captions" },
    xs: { px: 12, rem: "0.75rem", use: "Metadata (year, rating, duration)" },
    sm: { px: 14, rem: "0.875rem", use: "Nav items, body small, table cells" },
    base: { px: 16, rem: "1rem", use: "Body text, descriptions" },
    lg: { px: 18, rem: "1.125rem", use: "Section headers, card titles" },
    xl: { px: 20, rem: "1.25rem", use: "Page titles" },
    "2xl": { px: 24, rem: "1.5rem", use: "Section hero titles" },
    "3xl": { px: 30, rem: "1.875rem", use: "Feature titles" },
    "4xl": { px: 36, rem: "2.25rem", use: "Hero titles (mobile)" },
    "5xl": { px: 48, rem: "3rem", use: "Hero titles (desktop)" },
    "6xl": { px: 60, rem: "3.75rem", use: "Cinematic display titles" },
    "7xl": { px: 72, rem: "4.5rem", use: "Large cinematic titles (desktop)" },
    "8xl": { px: 96, rem: "6rem", use: "Full-bleed cinematic display (desktop, short titles)" },
  },

  /**
   * Weight mapping for hierarchy:
   *   normal(400)   — Body text, descriptions
   *   medium(500)   — Nav items, labels
   *   semibold(600) — Section headers, card titles
   *   bold(700)     — Page titles, buttons
   *   extrabold(800)— Hero/display titles, brand text
   *   black(900)    — Cinematic display (7xl/8xl only, --font-display)
   */
  weights: {
    normal: 400,
    medium: 500,
    semibold: 600,
    bold: 700,
    extrabold: 800,
    black: 900,
  },
} as const;

// ────────────────────────────────────────────────────────────────
// § 4  LAYOUT & SPACING
// ────────────────────────────────────────────────────────────────

export const LAYOUT = {
  /** Sidebar dimensions */
  sidebar: {
    width: 260,
    collapsedWidth: 64,
  },

  /** Page-level padding (maps to Tailwind's px-* classes) */
  pagePadding: {
    mobile: "px-4", // 16px
    tablet: "px-6", // 24px
    desktop: "px-12", // 48px
  },

  /** Vertical section spacing */
  sectionGap: {
    mobile: "gap-6", // 24px
    desktop: "gap-8", // 32px
  },

  /** Media grid configuration */
  grid: {
    gap: {
      mobile: "gap-3", // 12px
      desktop: "gap-4", // 16px
    },
    posterColumns: {
      mobile: "grid-cols-2",
      sm: "sm:grid-cols-3",
      md: "md:grid-cols-4",
      lg: "lg:grid-cols-5",
      xl: "xl:grid-cols-6",
    },
    backdropColumns: {
      mobile: "grid-cols-1",
      sm: "sm:grid-cols-2",
      lg: "lg:grid-cols-3",
    },
  },

  /** Hero section dimensions — cinematic, not banner-sized */
  hero: {
    height: {
      mobile: "h-[50vh]",
      desktop: "h-[60vh]",
    },
    maxHeight: "max-h-[700px]",
    minHeight: "min-h-[350px]",
  },

  /** Detail page hero — even more immersive */
  detailHero: {
    height: {
      mobile: "h-[50vh]",
      desktop: "h-[65vh]",
    },
    maxHeight: "max-h-[800px]",
    minHeight: "min-h-[400px]",
  },
} as const;

// ────────────────────────────────────────────────────────────────
// § 5  MOTION & ANIMATION
// ────────────────────────────────────────────────────────────────

export const MOTION = {
  /**
   * Interaction duration tokens — exposed as CSS custom properties.
   * Use via: transition-all duration-(--duration-normal)
   */
  duration: {
    instant: "0ms",
    fast: "150ms", // Micro-interactions (hover, press)
    normal: "250ms", // Standard transitions (expand, slide)
    slow: "400ms", // Page transitions, hero crossfade
    glacial: "700ms", // Dramatic reveals, full-page animations
  },

  /**
   * Cinematic duration tokens — for ambient, atmospheric motion.
   * These are intentionally much longer than interaction durations.
   */
  cinematicDuration: {
    cinematic: "1200ms", // Ambient color shifts, atmosphere changes
    drift: "2000ms", // Slow ambient transitions (glow pulse, color fade)
    kenBurnsMin: "12000ms", // Ken Burns cycle minimum
    kenBurnsMax: "18000ms", // Ken Burns cycle maximum
  },

  /**
   * Easing curves — exposed as CSS custom properties.
   * Use via: ease-(--ease-smooth)
   */
  easing: {
    default: "cubic-bezier(0.4, 0, 0.2, 1)", // General purpose
    in: "cubic-bezier(0.4, 0, 1, 1)", // Entering (accelerate)
    out: "cubic-bezier(0, 0, 0.2, 1)", // Exiting (decelerate)
    inOut: "cubic-bezier(0.4, 0, 0.2, 1)", // Symmetric
    spring: "cubic-bezier(0.34, 1.56, 0.64, 1)", // Bouncy (scale, pop-in)
    gentle: "cubic-bezier(0.25, 0, 0.35, 1)", // Slow atmospheric transitions
  },

  /**
   * Interaction animation patterns:
   *
   * Card hover:       scale(1.03) + brightness, 150ms
   * Page enter:       opacity 0→1 + translateY(8px→0), 300ms ease-out
   * Sidebar toggle:   width transition, 250ms ease-in-out
   * Modal enter:      opacity + scale(0.95→1), 200ms spring
   * Hero crossfade:   opacity 0→1, 500ms ease
   * Drawer slide:     translateX(-100%→0), 300ms ease-out
   *
   * Cinematic animation patterns:
   *
   * Ken Burns:        scale(1)→scale(1.05) + slight translate, 12-18s linear
   * Ambient glow:     radial gradient opacity pulse, 2s gentle ease
   * Ambient color:    --ambient transition, 1200ms gentle ease
   * Parallax:         backdrop at 0.3-0.5x scroll speed
   */
} as const;

// ────────────────────────────────────────────────────────────────
// § 6  BORDER RADIUS
// ────────────────────────────────────────────────────────────────

export const RADII = {
  /**
   * Radius scale — exposed as CSS custom properties.
   * Base --radius is set per-theme (typically 0.5rem or 0.75rem).
   *
   * Token          Typical px   Use case
   * ─────────────  ──────────   ────────────────────────────────
   * --radius-sm    4px          Badges, small tags
   * --radius-md    6px          Buttons, inputs
   * --radius-lg    8px          Cards, panels
   * --radius-xl    12px         Large cards, modals
   * --radius-2xl   16px         Hero sections, feature cards
   * --radius-pill  9999px       Pill buttons, filter chips, tags
   */
} as const;

// ────────────────────────────────────────────────────────────────
// § 7  SHADOWS
// ────────────────────────────────────────────────────────────────

export const SHADOWS = {
  /**
   * Shadow scale — exposed as CSS custom properties.
   * On dark UIs, shadows are subtle. Depth is primarily
   * communicated through surface color, not shadow.
   *
   * Token          Use case
   * ─────────────  ────────────────────────────────────────────
   * --shadow-sm    Subtle lift (cards on dark bg)
   * --shadow-md    Dropdowns, popovers
   * --shadow-lg    Modals, elevated panels
   * --shadow-xl    Floating controls, toasts
   * --shadow-glow  Accent glow (optional, uses primary color)
   */
} as const;

// ────────────────────────────────────────────────────────────────
// § 8  Z-INDEX SCALE
// ────────────────────────────────────────────────────────────────

export const Z_INDEX = {
  base: 0, // Default stacking
  dropdown: 10, // Dropdown menus
  sticky: 20, // Sticky headers, sidebar
  fixed: 30, // Fixed elements
  overlay: 40, // Page overlays, backdrop
  modal: 50, // Modal dialogs
  popover: 60, // Popovers, tooltips
  toast: 70, // Toast notifications
  tooltip: 80, // Tooltip (topmost)
} as const;

// ────────────────────────────────────────────────────────────────
// § 9  COMPONENT PATTERNS
// ────────────────────────────────────────────────────────────────
//
// Standard UI patterns and their implementation notes.
// Utility classes for these are defined in app.css.
//
// ┌─────────────────────────────────────────────────────────────┐
// │  HERO SECTION                                              │
// │  ─────────────────────────────────────────────              │
// │  Full-bleed backdrop image with gradient overlay.          │
// │  Title in font-display at text-5xl/6xl.                    │
// │  Metadata in small badges. Play button in pill shape.      │
// │  CSS: .hero-gradient on overlay div                        │
// │                                                            │
// │  ┌──────────────────────────────────────────┐              │
// │  │  ░░░░░ backdrop image ░░░░░░░░░░░░░░░░░ │              │
// │  │  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │              │
// │  │  ▓▓▓▓▓▓▓ gradient ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ │              │
// │  │  TITLE                                   │              │
// │  │  metadata • metadata • metadata          │              │
// │  │  [▶ Play]                                │              │
// │  └──────────────────────────────────────────┘              │
// ├─────────────────────────────────────────────────────────────┤
// │  MEDIA CARD                                                │
// │  ─────────────────────────────────────────────              │
// │  Poster image with rounded-xl corners.                     │
// │  Title + year beneath. Hover: .media-card effect           │
// │  (subtle scale + brightness lift).                         │
// │                                                            │
// │  ┌──────────┐                                              │
// │  │          │                                              │
// │  │  poster  │   aspect-[2/3] for portrait                  │
// │  │  image   │   aspect-video for landscape                 │
// │  │          │                                              │
// │  └──────────┘                                              │
// │  Title                                                     │
// │  Year                                                      │
// ├─────────────────────────────────────────────────────────────┤
// │  GLASS SURFACE                                             │
// │  ─────────────────────────────────────────────              │
// │  Semi-transparent bg with backdrop-blur.                   │
// │  CSS: .glass (strong) or .glass-subtle (light)             │
// │  Use for: floating controls, overlay panels, pills on hero │
// ├─────────────────────────────────────────────────────────────┤
// │  PILL BUTTON                                               │
// │  ─────────────────────────────────────────────              │
// │  Fully-rounded button for primary CTAs.                    │
// │  CSS: rounded-full or .pill                                │
// │  Use for: Play button, filter chips, tabs                  │
// ├─────────────────────────────────────────────────────────────┤
// │  EPISODE ROW                                               │
// │  ─────────────────────────────────────────────              │
// │  Horizontal: thumbnail + (episode# + title + desc + time)  │
// │  Subtle border-b separator. Hover: bg highlight.           │
// │  CSS: .episode-row                                         │
// │                                                            │
// │  ┌─────────┐ E1  Title                                    │
// │  │  thumb  │ Description text here...                      │
// │  └─────────┘ 52:30                                        │
// │  ─────────────────────────────────────────────             │
// ├─────────────────────────────────────────────────────────────┤
// │  MEDIA CAROUSEL ROW                                         │
// │  ─────────────────────────────────────────────              │
// │  Primary discovery pattern. Horizontal scrollable strip    │
// │  of poster or backdrop cards with section header above.    │
// │  Items peek at edges to signal scrollability.              │
// │  Scroll buttons appear on hover at row edges.              │
// │  CSS: scroll-smooth, snap-x, snap-mandatory on container  │
// │       snap-start on items, gap-3 md:gap-4                  │
// │                                                            │
// │  Section Header              [View All →]                  │
// │  ┌────┐ ┌────┐ ┌────┐ ┌────┐ ┌────┐ ┌──                  │
// │  │    │ │    │ │    │ │    │ │    │ │                       │
// │  │    │ │    │ │    │ │    │ │    │ │ peek                  │
// │  └────┘ └────┘ └────┘ └────┘ └────┘ └──                   │
// ├─────────────────────────────────────────────────────────────┤
// │  MEDIA DETAIL PAGE                                          │
// │  ─────────────────────────────────────────────              │
// │  Full-bleed hero (75-85vh) with ambient color.             │
// │  Sidebar auto-collapses for immersion.                     │
// │  --ambient set from backdrop artwork.                      │
// │  Info: title → tagline → metadata → overview → actions     │
// │  Below fold: cast carousel, episodes, recommendations     │
// │                                                            │
// │  ┌──────────────────────────────────────────┐              │
// │  │  ░░░░░ full-bleed backdrop ░░░░░░░░░░░░ │              │
// │  │  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ │ 75-85vh      │
// │  │  ▓▓▓ gradient + ambient glow ▓▓▓▓▓▓▓▓▓ │              │
// │  │  TITLE (display, 5xl-7xl)               │              │
// │  │  tagline • year • rating • runtime      │              │
// │  │  Overview paragraph...                  │              │
// │  │  [▶ Play]  [+ List]  [★ Rate]           │              │
// │  └──────────────────────────────────────────┘              │
// │  Cast Carousel ────────────────────────────                │
// │  Season/Episode Carousel ──────────────────                │
// │  Recommendations Carousel ─────────────────                │
// ├─────────────────────────────────────────────────────────────┤
// │  SIDEBAR NAVIGATION                                        │
// │  ─────────────────────────────────────────────              │
// │  Desktop: persistent left panel (260px)                    │
// │  Mobile: slide-out drawer                                  │
// │  Sections: grouped with small uppercase labels             │
// │  Active: bg-sidebar-accent + text-foreground               │
// │  Inactive: text-muted-foreground                           │
// │  Items: icon + label, py-2.5 px-3, rounded-lg             │
// │  At Detail immersion level: auto-collapse for full stage   │
// └─────────────────────────────────────────────────────────────┘
//

// ────────────────────────────────────────────────────────────────
// § 10  EXPORTS & UTILITIES
// ────────────────────────────────────────────────────────────────

/**
 * Standard poster grid class string.
 * Use: <div className={POSTER_GRID}>
 */
export const POSTER_GRID =
  "grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-3 md:gap-4";

/**
 * Standard backdrop (landscape) grid class string.
 */
export const BACKDROP_GRID = "grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3 md:gap-4";

/**
 * Standard section header styles.
 */
export const SECTION_HEADER = "text-lg font-semibold text-foreground";

/**
 * Standard metadata text styles.
 */
export const METADATA_TEXT = "text-xs text-muted-foreground";

/**
 * Standard page container with responsive padding.
 */
export const PAGE_CONTAINER = "px-4 py-4 sm:px-6 lg:px-12 lg:py-6";

/**
 * Hero banner container sizing — shared between HeroBanner and Home skeletons.
 */
export const HERO_BANNER_SIZE = "h-[50vh] min-h-[350px] max-h-[700px] lg:h-[60vh]";

/**
 * Taller cinematic hero sizing used by the Library page Recommended tab.
 * The marquee header sits on top, so the hero needs extra height to
 * keep the title block comfortably clear of the nav bar.
 */
export const HERO_BANNER_SIZE_TALL = "h-[60vh] min-h-[420px] max-h-[760px] lg:h-[72vh]";
