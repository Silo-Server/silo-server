import { t } from "@/i18n";

import { OVERLAY_CATEGORIES, type OverlayCategory, type OverlayPosition } from "./types";

// Shared UI metadata so user-facing and admin settings stay in sync. These
// labels are presentational only; the canonical enums live in types.ts.

export const POSITION_OPTIONS: { value: OverlayPosition; label: string }[] = [
  {
    value: "top-left",
    get label() {
      return t("web.settings.overlays.position.topLeft");
    },
  },
  {
    value: "left-edge",
    get label() {
      return t("web.settings.overlays.position.leftEdge");
    },
  },
  {
    value: "right-edge",
    get label() {
      return t("web.settings.overlays.position.rightEdge");
    },
  },
  {
    value: "bottom-left",
    get label() {
      return t("web.settings.overlays.position.bottomLeft");
    },
  },
  {
    value: "bottom-right",
    get label() {
      return t("web.settings.overlays.position.bottomRight");
    },
  },
];

interface CategoryMeta {
  category: OverlayCategory;
  title: string;
  description: string;
}

export const CATEGORY_META: Record<OverlayCategory, CategoryMeta> = {
  tech: {
    category: "tech",
    get title() {
      return t("web.settings.overlays.category.tech.title");
    },
    get description() {
      return t("web.settings.overlays.category.tech.description");
    },
  },
  ratings: {
    category: "ratings",
    get title() {
      return t("web.settings.overlays.category.ratings.title");
    },
    get description() {
      return t("web.settings.overlays.category.ratings.description");
    },
  },
  metadata: {
    category: "metadata",
    get title() {
      return t("web.settings.overlays.category.metadata.title");
    },
    get description() {
      return t("web.settings.overlays.category.metadata.description");
    },
  },
  ribbons: {
    category: "ribbons",
    get title() {
      return t("web.settings.overlays.category.ribbons.title");
    },
    get description() {
      return t("web.settings.overlays.category.ribbons.description");
    },
  },
};

// Iteration-friendly ordered list of category metadata.
export const CATEGORY_GROUPS: CategoryMeta[] = OVERLAY_CATEGORIES.map((c) => CATEGORY_META[c]);
