import type { GuidedFormState } from "@/components/collections/CollectionGuidedRulesEditor";
import { t } from "@/i18n";
import { formatLanguage } from "@/lib/languageDisplay";

export interface ActiveFilterBadge {
  key: string;
  label: string;
  clearPatch: Partial<GuidedFormState>;
}

/**
 * Compute displayable badge descriptors for all active "secondary" filters.
 * Skips mediaScope, sortField, sortOrder (shown inline in the bar).
 * Labels are in the current app language; a component showing them re-renders
 * on a language change (`useAppLanguage()`).
 */
export function getActiveFilterBadges(
  state: GuidedFormState,
  options: { isAudiobookLibrary?: boolean } = {},
): ActiveFilterBadge[] {
  const badges: ActiveFilterBadge[] = [];

  // Genres — one badge per selected genre
  for (const genre of state.genres) {
    badges.push({
      key: `genre:${genre}`,
      label: t("web.catalog.filters.badgeGenre", { genre }),
      clearPatch: { genres: state.genres.filter((g) => g !== genre) },
    });
  }

  // Year range — combine into a single badge
  if (state.yearFrom && state.yearTo) {
    badges.push({
      key: "year",
      label: t("web.catalog.filters.badgeYearRange", { from: state.yearFrom, to: state.yearTo }),
      clearPatch: { yearFrom: "", yearTo: "" },
    });
  } else if (state.yearFrom) {
    badges.push({
      key: "year",
      label: t("web.catalog.filters.badgeYearFrom", { year: state.yearFrom }),
      clearPatch: { yearFrom: "" },
    });
  } else if (state.yearTo) {
    badges.push({
      key: "year",
      label: t("web.catalog.filters.badgeYearTo", { year: state.yearTo }),
      clearPatch: { yearTo: "" },
    });
  }

  // Minimum IMDb rating
  if (state.minRating) {
    badges.push({
      key: "minRating",
      label: t("web.catalog.filters.badgeMinImdb", { rating: state.minRating }),
      clearPatch: { minRating: "" },
    });
  }

  // Content rating
  if (state.contentRating) {
    badges.push({
      key: "contentRating",
      label: t("web.catalog.filters.badgeContentRating", { rating: state.contentRating }),
      clearPatch: { contentRating: "" },
    });
  }

  for (const lang of state.originalLanguages) {
    badges.push({
      key: `originalLanguage:${lang}`,
      label: t("web.catalog.filters.badgeLanguage", { language: formatLanguage(lang) }),
      clearPatch: {
        originalLanguages: state.originalLanguages.filter((l) => l !== lang),
      },
    });
  }

  if (state.actor) {
    badges.push({
      key: "actor",
      label: t("web.catalog.filters.badgeActor", { name: state.actor }),
      clearPatch: { actor: "" },
    });
  }

  if (state.director) {
    badges.push({
      key: "director",
      label: t("web.catalog.filters.badgeDirector", { name: state.director }),
      clearPatch: { director: "" },
    });
  }

  if (state.writer) {
    badges.push({
      key: "writer",
      label: t("web.catalog.filters.badgeWriter", { name: state.writer }),
      clearPatch: { writer: "" },
    });
  }

  if (state.producer) {
    badges.push({
      key: "producer",
      label: t("web.catalog.filters.badgeProducer", { name: state.producer }),
      clearPatch: { producer: "" },
    });
  }

  if (state.author) {
    badges.push({
      key: "author",
      label: t("web.catalog.filters.badgeAuthor", { name: state.author }),
      clearPatch: { author: "" },
    });
  }

  if (state.narrator && state.mediaScope === "audiobook") {
    badges.push({
      key: "narrator",
      label: t("web.catalog.filters.badgeNarrator", { name: state.narrator }),
      clearPatch: { narrator: "" },
    });
  }

  if (state.series) {
    badges.push({
      key: "series",
      label: t("web.catalog.filters.badgeSeries", { name: state.series }),
      clearPatch: { series: "" },
    });
  }

  // Studio
  if (state.studio) {
    badges.push({
      key: "studio",
      label: t("web.catalog.filters.badgeStudio", { name: state.studio }),
      clearPatch: { studio: "" },
    });
  }

  // Network
  if (state.network) {
    badges.push({
      key: "network",
      label: t("web.catalog.filters.badgeNetwork", { name: state.network }),
      clearPatch: { network: "" },
    });
  }

  // Country
  if (state.country) {
    badges.push({
      key: "country",
      label: t("web.catalog.filters.badgeCountry", { country: state.country }),
      clearPatch: { country: "" },
    });
  }

  // Status
  if (state.status) {
    badges.push({
      key: "status",
      label: t("web.catalog.filters.badgeMatch", { status: state.status }),
      clearPatch: { status: "" },
    });
  }

  if (state.watchStatus) {
    const statusKey = options.isAudiobookLibrary
      ? "web.catalog.filters.badgeListening"
      : state.mediaScope === "ebook"
        ? "web.catalog.filters.badgeRead"
        : "web.catalog.filters.badgeWatch";
    badges.push({
      key: "watchStatus",
      label: t(statusKey, { status: state.watchStatus }),
      clearPatch: { watchStatus: "" },
    });
  }

  // Added in the last
  if (state.addedInLast) {
    badges.push({
      key: "addedInLast",
      label: t("web.catalog.filters.badgeAddedInLast", { span: state.addedInLast }),
      clearPatch: { addedInLast: "" },
    });
  }

  // Released in the last
  if (state.releasedInLast) {
    badges.push({
      key: "releasedInLast",
      label: t("web.catalog.filters.badgeReleasedInLast", { span: state.releasedInLast }),
      clearPatch: { releasedInLast: "" },
    });
  }

  if (state.fourK) {
    badges.push({
      key: "fourK",
      label: "4K",
      clearPatch: { fourK: false },
    });
  }

  if (state.hdr) {
    badges.push({
      key: "hdr",
      label: "HDR",
      clearPatch: { hdr: false },
    });
  }

  if (state.dolbyVision) {
    badges.push({
      key: "dolbyVision",
      label: "DOVI",
      clearPatch: { dolbyVision: false },
    });
  }

  return badges;
}
