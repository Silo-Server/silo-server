import { OVERLAY_CATEGORIES, type OverlayCategory, type OverlayPosition } from "./types";

// Shared UI metadata so user-facing and admin settings stay in sync. These
// labels are presentational only; the canonical enums live in types.ts.

export const POSITION_OPTIONS: { value: OverlayPosition; label: string }[] = [
  { value: "top-left", label: "Top Left" },
  { value: "top-right", label: "Top Right" },
  { value: "bottom-left", label: "Bottom Left" },
  { value: "bottom-right", label: "Bottom Right" },
];

interface CategoryMeta {
  category: OverlayCategory;
  title: string;
  description: string;
}

export const CATEGORY_META: Record<OverlayCategory, CategoryMeta> = {
  tech: {
    category: "tech",
    title: "Media Info",
    description: "Technical details from your media files.",
  },
  ratings: {
    category: "ratings",
    title: "Ratings & Certifications",
    description: "Scores from external sources and content ratings.",
  },
  metadata: {
    category: "metadata",
    title: "Content Metadata",
    description: "Information about the content itself.",
  },
  ribbons: {
    category: "ribbons",
    title: "Status & Awards",
    description:
      "Series lifecycle, request status and award badges. Some require upcoming data sources.",
  },
};

// Iteration-friendly ordered list of category metadata.
export const CATEGORY_GROUPS: CategoryMeta[] = OVERLAY_CATEGORIES.map((c) => CATEGORY_META[c]);
