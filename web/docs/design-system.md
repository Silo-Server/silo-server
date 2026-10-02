# Silo Design System

Human-facing reference for the Silo web app's design language.

Code-backed tokens and utility constants live in [../src/lib/design-system.ts](../src/lib/design-system.ts). CSS tokens and reusable utility classes live in [../src/app.css](../src/app.css).

## Philosophy

Silo uses a cinema-first design language for media streaming interfaces. Media consumption and discovery are the primary concerns — every design decision should be evaluated against them.

The UI should feel like a dark theater, not a dashboard. Media artwork, backdrops, and thumbnails carry most of the visual weight. Interface chrome stays restrained so content stays primary. When a user lands on the app, the first thing they should feel is the content — a sweeping backdrop, a cinematic title, artwork that fills the viewport.

Themes should let content imagery be the primary source of color. Theme accent colors should complement, not compete with, poster and backdrop artwork. The default theme (Midnight Cinema) uses a monochromatic palette specifically so the media provides all the color.

## How To Use It

1. Colors: always use semantic CSS variables, never raw hex values.
   Good: `bg-background text-foreground`
   Good: `bg-surface text-muted-foreground`
   Avoid: `bg-[#1a1a1f]`
2. Spacing: use the existing Tailwind spacing scale and shared layout constants.
3. Typography: use the defined type scale. Hero and display text should use `--font-display`; everything else should default to `--font-body`.
4. Motion: use duration and easing variables for transitions instead of one-off timings.
5. Patterns: prefer the existing utility classes in `app.css` before inventing new presentation patterns.

## Design Principles

### 1. Content Is King

Media artwork and imagery are the primary visual elements. UI chrome recedes to let content shine.

Examples:
- Hero sections use full-bleed backdrop images.
- Card grids let poster art dominate; text labels are secondary.
- Navigation and controls use low-contrast, minimal styling.

### 2. Cinematic Darkness

Deep, dark surfaces create a theater-like atmosphere. Multiple darkness layers establish depth without harsh contrast.

Examples:
- Page backgrounds sit in the near-black range.
- Sidebars are darker than the main background.
- Cards use subtle surface lift rather than dramatic contrast.

### 3. Subtle Chrome

Borders, dividers, and backgrounds should stay low-contrast. Interactivity should come from opacity and surface shifts more than outlines or heavy shadows.

Examples:
- Borders are thin and close in value to the surrounding surface.
- Hover states shift background before they add stronger effects.
- Active states use a soft background highlight and text-color change.

### 4. Clear Hierarchy

Importance should come from size, weight, and opacity before color. Reserve accent color for actions and cues.

Examples:
- Hero titles are the largest and heaviest text on the page.
- Section headers are clearly smaller than page and hero titles.
- Metadata stays small and muted.
- Primary actions should be the most visually prominent controls.

### 5. Ambient Immersion

The UI should feel alive with the content it displays. Featured artwork should influence the surrounding atmosphere through subtle color bleed and ambient glow. The page should feel like it belongs to the content currently on screen.

Examples:
- Hero backdrops emit a soft ambient glow that tints the area around them via `--ambient` color.
- Detail pages feel like they belong to the featured title — the atmosphere shifts.
- Hero images use subtle Ken Burns motion (slow zoom/pan) to feel alive rather than static.
- Transitions between featured items shift the ambient color smoothly.

### 6. Discovery-First Layout

Horizontal scroll rows are the primary discovery pattern. Users browse by scanning rows of artwork, not by reading lists or navigating grids. Grids are reserved for search results, "view all" pages, and library management.

Examples:
- Home and discover pages use carousel rows as the primary content structure.
- Each row shows partially visible items at the edges to signal scrollability.
- Row items are large enough for artwork to read clearly at a glance.
- Section headers label each row; a "View All" link leads to the full grid.

### 7. Fluid Motion

Motion should guide attention without calling attention to itself. Prefer CSS transitions over JavaScript animation where possible. Ambient cinematic motion (Ken Burns, parallax, color shifts) uses longer durations and gentler easing than interaction motion.

Examples:
- Page transitions use fade plus slight slide.
- Card hover uses subtle scale and brightness lift.
- Sidebar collapse expands and contracts smoothly.
- Hero transitions should crossfade rather than snap.
- Hero backdrops use slow Ken Burns zoom (10-15 seconds, linear easing).
- Ambient color shifts transition over 1-2 seconds with gentle easing.

### 8. Immersion Levels

The UI adapts its density and chrome based on how deeply a user is engaged with content. Deeper engagement means less UI and more content.

| Level | Context | Behavior |
|-------|---------|----------|
| Browse | Home, discover, library | Sidebar visible, multiple rows, standard hero |
| Focus | Hovering or previewing a title | Surrounding content dims, focused item expands |
| Detail | Movie or show page | Full-bleed hero, sidebar recedes, page is about this content |
| Watch | Active playback | UI fully hidden, minimal transport controls on interaction |

### 9. Responsive Density

Information density should adapt to viewport size instead of shrinking the desktop layout unchanged.

Examples:
- Desktop uses persistent sidebar navigation and wider grids.
- Tablet reduces column count and can collapse secondary navigation.
- Mobile stacks content and uses drawers or sheets where needed.

## Color Architecture

Colors are organized as semantic layers. Theme files provide the actual values; components should reference tokens rather than raw colors.

### Surface Hierarchy

- `--sidebar`: navigation background
- `--background`: page background
- `--surface`: cards, panels, default surfaces
- `--surface-hover`: hover and focus state surface
- `--surface-raised`: elevated elements like modals and popovers
- `--card`: card backgrounds
- `--popover`: dropdown and popover backgrounds

### Text Hierarchy

- `--foreground`: primary text
- `--card-foreground`: text on cards
- `--muted-foreground`: secondary and helper text

### Interactive Tokens

- `--primary`: primary action color
- `--primary-foreground`: text on primary elements
- `--secondary`: secondary element backgrounds
- `--accent`: highlighted and active backgrounds
- `--destructive`: destructive actions
- `--ring`: focus ring

### Ambient Tokens

- `--ambient`: dominant color extracted from the currently featured artwork. Set dynamically via JavaScript. Used for subtle background glow and atmosphere. Falls back to `--primary` when no artwork color is available.
- `--ambient-glow`: a low-opacity radial gradient using `--ambient` placed behind hero sections and detail page headers. Should be barely perceptible — felt more than seen.

### Border Tokens

- `--border`: default borders
- `--input`: input field border and background

## Typography

### Families

- `--font-body`: primary UI font
- `--font-display`: hero and cinematic display text
- `--font-mono`: code and technical data

### Type Scale

- `2xs`: badge labels, tiny captions
- `xs`: metadata such as year, rating, duration
- `sm`: nav items, small body text, table cells
- `base`: body text and descriptions
- `lg`: section headers and card titles
- `xl`: page titles
- `2xl`: section hero titles
- `3xl`: feature titles
- `4xl`: mobile hero titles
- `5xl`: desktop hero titles
- `6xl`: cinematic display titles
- `7xl`: large cinematic titles (desktop only, used selectively)
- `8xl`: full-bleed cinematic display (desktop only, short titles and single words)

### Weight Guidance

- `400`: body text and descriptions
- `500`: nav items and labels
- `600`: section headers and card titles
- `700`: page titles and buttons
- `800`: hero titles and brand text
- `900`: cinematic display titles (7xl/8xl only, use with `--font-display`)

## Layout And Spacing

- Sidebar width: `260px`
- Collapsed sidebar width: `64px`
- Page padding grows from mobile to desktop
- Section gaps expand slightly on larger screens
- Poster grids scale from 2 columns on mobile up to 6 on large desktop (used for search results and "view all" pages)
- Backdrop grids scale from 1 to 3 columns
- Carousel rows are the primary discovery layout; grids are secondary
- Hero sections should command the viewport on first load — the hero is the first impression

### Hero Sizing

The hero should feel cinematic, not like a banner. On first load the hero should dominate the viewport, with content rows discoverable via scroll.

- Mobile: `h-[60vh]`, `min-h-[400px]`
- Desktop: `h-[75vh]`, `min-h-[500px]`, `max-h-[900px]`
- Detail page heroes can be taller since they are the entire context for the page

## Motion

### Interaction Durations

- `instant`: `0ms`
- `fast`: `150ms` — micro-interactions (hover, press)
- `normal`: `250ms` — standard transitions (expand, slide)
- `slow`: `400ms` — page transitions, hero crossfade
- `glacial`: `700ms` — dramatic reveals, full-page animations

### Cinematic Durations

- `cinematic`: `1200ms` — ambient color shifts, atmosphere changes
- `drift`: `2000ms` — slow ambient transitions (glow pulse, color fade)
- `kenBurns`: `12000ms`–`18000ms` — slow zoom/pan on hero backdrops

### Easing

- `default`: general-purpose transitions
- `in`: entering acceleration
- `out`: exit deceleration
- `inOut`: symmetric transitions
- `spring`: bouncy scale or pop-in moments
- `linear`: ambient cinematic motion (Ken Burns, glow pulse)
- `gentle`: `cubic-bezier(0.25, 0, 0.35, 1)` — slow atmospheric transitions

### Interaction Patterns

- Card hover: subtle scale and brightness lift (`fast`)
- Page enter: opacity plus slight upward movement (`slow`)
- Sidebar toggle: width transition (`normal`)
- Modal enter: opacity plus scale-in (`normal`, `spring`)
- Hero crossfade: opacity transition (`slow`)
- Drawer slide: horizontal translate (`normal`)

### Cinematic Patterns

- Ken Burns: slow zoom from `scale(1)` to `scale(1.05)` with slight translate, `linear` easing, 12-18 second cycle
- Ambient glow: low-opacity radial gradient using `--ambient`, transitions on `cinematic` duration
- Hero carousel crossfade: opacity transition at `slow` duration, ambient color shifts at `cinematic` duration
- Parallax: backdrop moves at 0.3-0.5x scroll speed relative to overlay content

## Radius, Shadow, And Layering

### Radius

Use a small shared radius scale rather than arbitrary values:
- small radii for badges and tags
- medium radii for buttons and inputs
- large radii for cards and panels
- extra-large radii for hero modules and large surfaces
- pill radius for chips, tabs, and play buttons

### Shadows

On dark surfaces, depth should come mostly from surface color, not heavy shadow. Shadows should stay restrained and be reserved for overlays, modals, and floating controls.

### Z-Index

Use a consistent layering model:
- base content
- dropdowns
- sticky regions
- fixed elements
- overlays
- modals
- popovers
- toasts
- tooltips

## Component Patterns

### Hero Section

Full-bleed backdrop image that commands the viewport. Gradient overlays dissolve the image into the page. Large display title, compact metadata, and a pill-shaped primary action. The hero is the cinematic first impression.

Implementation cues:
- Use `.hero-gradient` for the bottom-to-top fade, `.hero-gradient-radial` for vignette, `.hero-gradient-left` for sidebar-edge fade.
- Use display typography (`--font-display`) at `text-5xl` to `text-7xl` for the title.
- Keep metadata compact and secondary (badges, small text).
- Apply Ken Burns motion to the backdrop image for ambient life.
- Set `--ambient` from the dominant backdrop color for atmosphere glow.
- Height should be `75vh` on desktop — cinematic, not a banner.

### Media Carousel Row

Horizontal scroll rows are the primary discovery pattern. Each row presents a scrollable strip of poster or backdrop cards with a section header above.

Implementation cues:
- Items at the left and right edges should be partially visible to signal scrollability (peek).
- Scroll buttons appear on hover at the row edges with a gradient mask.
- Use `scroll-smooth` with `snap-x snap-mandatory` on the container; `snap-start` on items.
- Gap between items: `gap-3` mobile, `gap-4` desktop.
- Poster items: `aspect-[2/3]`, minimum width ~150px.
- Backdrop items: `aspect-video`, minimum width ~280px.
- Section header sits above the row with optional icon and "View All" link.

### Media Card

Poster or backdrop artwork should dominate. Supporting text sits below and stays visually secondary.

Implementation cues:
- Use `.media-card` for hover effect (scale + brightness lift).
- Use `.media-card-image` for consistent rounded corners and surface background placeholder.
- Hover should slightly scale and brighten, not dramatically lift.
- Continue-watching cards add a `.progress-bar` at the bottom edge.
- Movie and series cards expose watched state as an eye toggle beside the favorite action on hover and keyboard focus; compact posters use smaller matching controls. Continue Watching uses the eye alone at the bottom-left. Watched episodes keep the muted circle-check beside the episode number below the thumbnail.

### Media Detail Page

The most important single page in the app. When a user selects a title, the page should feel like opening a collector's edition — fully immersive, cinematic, and dedicated to that content.

Implementation cues:
- Full-bleed backdrop hero, taller than home heroes (`75-85vh`).
- Set `--ambient` from the backdrop artwork to tint the page atmosphere.
- Sidebar should auto-collapse or recede to maximize immersion.
- Info architecture below the hero: tagline, metadata badges, overview text, action buttons, then supplementary sections (cast carousel, season/episode list, recommendations).
- Supplementary sections use carousel rows, not grids.
- The page should feel like it belongs to the content — the atmosphere changes.

### Glass Surface

Semi-transparent surfaces with blur should be used selectively for floating controls and overlay panels, not as the default card style.

Implementation cues:
- Use `.glass` for strong glass (floating controls, overlay panels).
- Use `.glass-subtle` for lighter glass (pills on hero, metadata overlays).
- Use `.glass-dark` for dark glass (navigation overlays, backdrop panels).

### Pill Button

Primary playback and filter controls should feel tactile and simple.

Implementation cues:
- Use `.pill-primary` for main CTAs (Play, Watch).
- Use `.pill-secondary` for secondary actions.
- Use `.pill-glass` for buttons floating over imagery.
- Reserve pill shape for primary actions, tabs, and chips.

### Episode Row

Episode rows should read quickly in a scan: thumbnail, number, title, description, runtime.

Implementation cues:
- Use `.episode-row` for consistent styling and hover behavior.
- Keep separators subtle (single `border-b`).
- Hover should highlight the row background without adding visual clutter.
- Active/playing episode should be visually distinct (highlight or indicator).

### Sidebar Navigation

Desktop uses a persistent left rail; mobile uses a drawer. Active items should be clear without becoming loud. At the Detail immersion level, the sidebar should recede to give content full stage.

Implementation cues:
- Group sections with small labels.
- Use icon plus label layout.
- Active state should rely on background and text treatment, not thick borders.
- Sidebar is the darkest surface (`--sidebar`), creating depth against the main content.

## Related Files

- [../src/lib/design-system.ts](../src/lib/design-system.ts): code-backed constants and shared class strings
- [../src/app.css](../src/app.css): CSS tokens and component utility classes
- [../src/lib/themes.ts](../src/lib/themes.ts): theme definitions and metadata
