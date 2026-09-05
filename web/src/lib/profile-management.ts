import type { CreateProfileRequest, Profile } from "@/api/types";
import type { ProfileUpdate } from "@/hooks/queries/profiles";
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
  libraries: string;
  playbackQuality: string;
  text: string;
}

export const CONTENT_RATING_OPTIONS: ContentRatingOption[] = [
  { value: "", label: "Any content", summary: "Any content" },
  { value: "G", label: "G / TV-G", summary: "G max" },
  { value: "PG", label: "PG / TV-PG / TV-Y7", summary: "PG max" },
  { value: "PG-13", label: "PG-13 / TV-14", summary: "PG-13 max" },
  { value: "R", label: "R / TV-MA / NC-17", summary: "R max" },
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
    maxPlaybackQuality: playbackQualityPresetFromValue(profile?.max_playback_quality),
    libraryRestrictionsEnabled: profile?.library_restrictions_enabled ?? false,
    allowedLibraryIDs: sortUniqueLibraryIDs(profile?.allowed_library_ids),
  };
}

export function buildProfileRequestFromDraft(draft: ProfileDraft): CreateProfileRequest {
  const body: CreateProfileRequest = {
    name: draft.name.trim(),
    avatar: draft.avatarPreset ? avatarPresetRef(draft.avatarPreset) : "",
    is_child: draft.isChild,
    max_content_rating: draft.maxContentRating,
    max_playback_quality: playbackQualityValueFromPreset(draft.maxPlaybackQuality),
    library_restrictions_enabled: draft.libraryRestrictionsEnabled,
    allowed_library_ids: draft.libraryRestrictionsEnabled
      ? sortUniqueLibraryIDs(draft.allowedLibraryIDs)
      : [],
  };

  if (draft.clearPin) {
    body.pin = "";
  } else if (draft.pin.trim() !== "") {
    body.pin = draft.pin.trim();
  }

  return body;
}

/**
 * The v2 PATCH body for an edited profile. Every field the editor owns is sent
 * so the profile matches the draft; a cleared value goes on the wire as null
 * (the contract's clearing form), and the PIN is omitted unless it changes.
 */
export function buildProfileUpdateFromDraft(draft: ProfileDraft): ProfileUpdate {
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
    | "library_restrictions_enabled"
    | "allowed_library_ids"
    | "max_playback_quality"
  >,
): ProfileAccessSummary {
  const contentRating =
    CONTENT_RATING_OPTIONS.find((option) => option.value === profile.max_content_rating)?.summary ??
    "Any content";
  const libraryCount = sortUniqueLibraryIDs(profile.allowed_library_ids).length;
  const libraries = profile.library_restrictions_enabled
    ? `${libraryCount} ${libraryCount === 1 ? "library" : "libraries"}`
    : "All libraries";
  const qualityLabel = formatPlaybackQualityPreset(profile.max_playback_quality);
  const playbackQuality = `${qualityLabel} quality`;

  return {
    contentRating,
    libraries,
    playbackQuality,
    text: [contentRating, libraries, playbackQuality].join(" · "),
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
    maxPlaybackQuality: "any",
    libraryRestrictionsEnabled: false,
    allowedLibraryIDs: [],
  };
}
