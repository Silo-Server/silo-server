import type { Profile } from "@/api/types";
import type { ProfileCreate, ProfileUpdate } from "@/hooks/queries/profiles";
import { t } from "@/i18n";
import { avatarPresetRef, parseProfileAvatarPresetRef } from "@/lib/profile-avatars";
import {
  formatPlaybackQualityPreset,
  playbackQualityPresetFromValue,
  playbackQualityValueFromPreset,
  type PlaybackQualityPreset,
} from "@/lib/playback-quality";

export interface ProfileDraft {
  name: string;
  avatarPreset: string;
  pin: string;
  clearPin: boolean;
  isChild: boolean;
  maxContentRating: string;
  /** Advisory-age limit; null means no limit. */
  maxAdvisoryAge: number | null;
  /** Hide titles with no advisory age; only meaningful with a limit. */
  requireAdvisoryAge: boolean;
  maxPlaybackQuality: PlaybackQualityPreset;
  libraryRestrictionsEnabled: boolean;
  allowedLibraryIDs: number[];
}

export interface ContentRatingOption {
  value: string;
  label: string;
  summary: string;
}

export interface ProfileAccessSummary {
  contentRating: string;
  /** "" when the profile has no advisory-age limit. */
  advisoryAge: string;
  libraries: string;
  playbackQuality: string;
  text: string;
}

/**
 * Maturity ceilings a profile can be given, ordered by the oldest minimum age
 * each admits. The server enforces the ceiling as an age, so a value from any
 * national system limits a library certified in any other: "12" admits BBFC
 * 12A, FSK 12 and PG12 alike.
 *
 * A US value admits its whole US tier, which is what the labels say: PG-13
 * admits TV-14, and R admits TV-MA and NC-17.
 */
export const CONTENT_RATING_OPTIONS: ContentRatingOption[] = [
  {
    value: "",
    get label() {
      return t("web.profiles.helpers.contentRating.any");
    },
    get summary() {
      return t("web.profiles.helpers.contentRating.any");
    },
  },
  ratingOption("G", "G / TV-G / U"),
  ratingOption("6", "6 / FSK 6"),
  ratingOption("PG", "PG / TV-PG / TV-Y7"),
  ratingOption("12", "12 / 12A / FSK 12"),
  ratingOption("PG-13", "PG-13 / TV-14"),
  ratingOption("15", "15 / MA 15+"),
  ratingOption("16", "16 / FSK 16"),
  ratingOption("R", "R / TV-MA / NC-17 / 18"),
];

/** A rating ceiling; `label` lists certificate codes, which are the same in every language. */
function ratingOption(value: string, label: string): ContentRatingOption {
  return {
    value,
    label,
    get summary() {
      return t("web.profiles.helpers.contentRating.max", { rating: value });
    },
  };
}

export interface AdvisoryAgeOption {
  /** null is "no limit". */
  value: number | null;
  label: string;
}

/** The advisory-age limits the server accepts (access.Min/MaxAdvisoryAgeLimit). */
export const MIN_ADVISORY_AGE_LIMIT = 1;
export const MAX_ADVISORY_AGE_LIMIT = 21;

/**
 * Advisory-age limits a profile can be given: every limit the server accepts,
 * so a profile set to any valid limit by another client opens on a matching
 * option. A limit of N hides titles recommended for viewers older than N.
 * Titles with no advisory age are not hidden by the limit unless the profile
 * also requires an advisory age, so the content rating still has to do its job.
 */
export const ADVISORY_AGE_OPTIONS: AdvisoryAgeOption[] = [
  {
    value: null,
    get label() {
      return t("web.profiles.helpers.advisoryAge.noLimit");
    },
  },
  ...Array.from({ length: MAX_ADVISORY_AGE_LIMIT - MIN_ADVISORY_AGE_LIMIT + 1 }, (_, index) => {
    const age = index + MIN_ADVISORY_AGE_LIMIT;
    return {
      value: age,
      get label() {
        return t("web.profiles.helpers.advisoryAge.agesAndUnder", { age, age_text: age });
      },
    };
  }),
];

function sortUniqueLibraryIDs(ids: number[] | null | undefined): number[] {
  if (!ids || ids.length === 0) {
    return [];
  }

  return [...new Set(ids.filter((id) => Number.isInteger(id) && id > 0))].sort((a, b) => a - b);
}

export function createProfileDraft(profile?: Profile | null): ProfileDraft {
  return {
    name: profile?.name ?? "",
    avatarPreset: parseProfileAvatarPresetRef(profile?.avatar),
    pin: "",
    clearPin: false,
    isChild: profile?.is_child ?? false,
    maxContentRating: profile?.max_content_rating ?? "",
    maxAdvisoryAge: profile?.max_advisory_age ?? null,
    requireAdvisoryAge: profile?.require_advisory_age ?? false,
    maxPlaybackQuality: playbackQualityPresetFromValue(profile?.max_playback_quality),
    libraryRestrictionsEnabled: profile?.library_restrictions_enabled ?? false,
    allowedLibraryIDs: sortUniqueLibraryIDs(profile?.allowed_library_ids),
  };
}

/**
 * The v2 POST body for a new profile. A member with nothing to set is omitted:
 * there is nothing to clear on a profile that does not exist yet, and the
 * contract rejects empty strings where v1 read them as "unset".
 */
export interface ProfileRequestOptions {
  /**
   * The server accepts `max_advisory_age` (`max_advisory_age_supported` on
   * listProfiles). Without it the member is never sent: an older server
   * rejects unknown members, which would fail every profile save.
   */
  advisoryAgeSupported?: boolean;
  /**
   * The server accepts `require_advisory_age`
   * (`require_advisory_age_supported`). Same reason: never send it otherwise.
   */
  requireAdvisoryAgeSupported?: boolean;
}

export function buildProfileRequestFromDraft(
  draft: ProfileDraft,
  options: ProfileRequestOptions = {},
): ProfileCreate {
  const maxPlaybackQuality = playbackQualityValueFromPreset(draft.maxPlaybackQuality);
  const body: ProfileCreate = {
    name: draft.name.trim(),
    is_child: draft.isChild,
    library_restrictions_enabled: draft.libraryRestrictionsEnabled,
    allowed_library_ids: draft.libraryRestrictionsEnabled
      ? sortUniqueLibraryIDs(draft.allowedLibraryIDs).map(String)
      : [],
  };

  if (draft.avatarPreset) {
    body.avatar = avatarPresetRef(draft.avatarPreset);
  }
  if (draft.maxContentRating !== "") {
    body.max_content_rating = draft.maxContentRating;
  }
  if (options.advisoryAgeSupported && draft.maxAdvisoryAge !== null) {
    body.max_advisory_age = draft.maxAdvisoryAge;
    if (options.requireAdvisoryAgeSupported && draft.requireAdvisoryAge) {
      body.require_advisory_age = true;
    }
  }
  if (maxPlaybackQuality === "1080p" || maxPlaybackQuality === "2160p") {
    body.max_playback_quality = maxPlaybackQuality;
  }
  if (!draft.clearPin && draft.pin.trim() !== "") {
    body.pin = draft.pin.trim();
  }

  return body;
}

/**
 * The v2 PATCH body for an edited profile. Every field the editor owns is sent
 * so the profile matches the draft; a cleared value goes on the wire as null
 * (the contract's clearing form), and the PIN is omitted unless it changes.
 */
export function buildProfileUpdateFromDraft(
  draft: ProfileDraft,
  options: ProfileRequestOptions = {},
): ProfileUpdate {
  const maxPlaybackQuality = playbackQualityValueFromPreset(draft.maxPlaybackQuality);
  const body: ProfileUpdate = {
    name: draft.name.trim(),
    avatar: draft.avatarPreset ? avatarPresetRef(draft.avatarPreset) : null,
    is_child: draft.isChild,
    max_content_rating: draft.maxContentRating === "" ? null : draft.maxContentRating,
    max_playback_quality:
      maxPlaybackQuality === "1080p" || maxPlaybackQuality === "2160p" ? maxPlaybackQuality : null,
    library_restrictions_enabled: draft.libraryRestrictionsEnabled,
    allowed_library_ids: draft.libraryRestrictionsEnabled
      ? sortUniqueLibraryIDs(draft.allowedLibraryIDs).map(String)
      : [],
  };

  if (options.advisoryAgeSupported) {
    body.max_advisory_age = draft.maxAdvisoryAge;
    if (options.requireAdvisoryAgeSupported) {
      // Cleared together with the limit, so removing the limit does not leave
      // a stored flag that silently comes back with the next limit.
      body.require_advisory_age = draft.maxAdvisoryAge !== null && draft.requireAdvisoryAge;
    }
  }

  if (draft.clearPin) {
    body.pin = null;
  } else if (draft.pin.trim() !== "") {
    body.pin = draft.pin.trim();
  }

  return body;
}

export function buildProfileAccessSummary(
  profile: Pick<
    Profile,
    | "max_content_rating"
    | "max_advisory_age"
    | "require_advisory_age"
    | "library_restrictions_enabled"
    | "allowed_library_ids"
    | "max_playback_quality"
  >,
): ProfileAccessSummary {
  const contentRating =
    CONTENT_RATING_OPTIONS.find((option) => option.value === profile.max_content_rating)?.summary ??
    t("web.profiles.helpers.contentRating.any");
  const advisoryAge = profile.max_advisory_age
    ? t(
        profile.require_advisory_age
          ? "web.profiles.helpers.access.advisoryAgeMaxRatedOnly"
          : "web.profiles.helpers.access.advisoryAgeMax",
        { age: profile.max_advisory_age },
      )
    : "";
  const libraryCount = sortUniqueLibraryIDs(profile.allowed_library_ids).length;
  const libraries = profile.library_restrictions_enabled
    ? t("web.profiles.helpers.access.libraries", {
        count: libraryCount,
        count_text: libraryCount,
      })
    : t("web.profiles.helpers.access.allLibraries");
  const playbackQuality =
    playbackQualityPresetFromValue(profile.max_playback_quality) === "any"
      ? t("web.profiles.helpers.access.anyQuality")
      : t("web.profiles.helpers.access.quality", {
          quality: formatPlaybackQualityPreset(profile.max_playback_quality),
        });

  return {
    contentRating,
    advisoryAge,
    libraries,
    playbackQuality,
    text: [contentRating, advisoryAge, libraries, playbackQuality].filter(Boolean).join(" · "),
  };
}

export function applyKidsPreset(
  draft: ProfileDraft,
  options: {
    contentRatingTouched: boolean;
    libraryAccessTouched: boolean;
  },
): ProfileDraft {
  const next: ProfileDraft = {
    ...draft,
    isChild: true,
  };

  if (!options.contentRatingTouched && next.maxContentRating === "") {
    next.maxContentRating = "PG";
  }

  if (!options.libraryAccessTouched && !next.libraryRestrictionsEnabled) {
    next.libraryRestrictionsEnabled = true;
    next.allowedLibraryIDs = [];
  }

  return next;
}

export function clearKidsPreset(draft: ProfileDraft): ProfileDraft {
  return {
    ...draft,
    isChild: false,
    maxContentRating: "",
    maxAdvisoryAge: null,
    requireAdvisoryAge: false,
    maxPlaybackQuality: "any",
    libraryRestrictionsEnabled: false,
    allowedLibraryIDs: [],
  };
}

/**
 * Whether the household has room for another profile. With no reported limit
 * (null) the answer is yes, so an older server keeps today's behaviour and
 * createProfile stays the authority.
 */
export function canAddProfile(profileCount: number, maxProfiles: number | null): boolean {
  return maxProfiles === null || profileCount < maxProfiles;
}

/**
 * Whether Switch Profile leads anywhere: to another profile, or to room for a
 * new one. A household with several profiles keeps it even at its limit.
 */
export function canSwitchProfile(profileCount: number, maxProfiles: number | null): boolean {
  return profileCount > 1 || canAddProfile(profileCount, maxProfiles);
}

/** The sentence shown in place of New profile once the household is full. */
export function profileLimitMessage(maxProfiles: number): string {
  return t("web.settings.profiles.limitReached", { count: maxProfiles });
}
