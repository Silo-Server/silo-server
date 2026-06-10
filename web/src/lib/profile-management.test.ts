import { describe, expect, it } from "vitest";

import type { Profile } from "@/api/types";
import { avatarPresetRef } from "@/lib/profile-avatars";
import {
  applyKidsPreset,
  buildProfileAccessSummary,
  clearKidsPreset,
  createProfileDraft,
} from "./profile-management";

function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "profile-1",
    name: "Main",
    avatar: "",
    has_pin: false,
    is_child: false,
    is_primary: false,
    max_content_rating: "",
    quality_preference: "auto",
    language: "en",
    subtitle_language: "",
    subtitle_mode: "auto",
    show_forced_subtitles: true,
    auto_skip_intro: false,
    auto_skip_credits: false,
    library_restrictions_enabled: false,
    allowed_library_ids: null,
    max_playback_quality: "",
    created_at: "2026-04-06T00:00:00Z",
    updated_at: "2026-04-06T00:00:00Z",
    ...overrides,
  };
}

describe("profile-management", () => {
  it("builds a readable access summary", () => {
    expect(
      buildProfileAccessSummary(
        makeProfile({
          max_content_rating: "PG",
          library_restrictions_enabled: true,
          allowed_library_ids: [2, 5],
          max_playback_quality: "1080p",
        }),
      ),
    ).toEqual({
      contentRating: "PG max",
      libraries: "2 libraries",
      playbackQuality: "Standard quality",
      text: "PG max · 2 libraries · Standard quality",
    });
  });

  it("seeds kids defaults from an unrestricted draft", () => {
    expect(
      applyKidsPreset(createProfileDraft(), {
        contentRatingTouched: false,
        libraryAccessTouched: false,
      }),
    ).toMatchObject({
      isChild: true,
      maxContentRating: "PG",
      libraryRestrictionsEnabled: true,
      allowedLibraryIDs: [],
    });
  });

  it("does not overwrite manual access changes when reapplying the kids preset", () => {
    const draft = createProfileDraft(
      makeProfile({
        max_content_rating: "PG-13",
        library_restrictions_enabled: true,
        allowed_library_ids: [9],
      }),
    );

    expect(
      applyKidsPreset(draft, {
        contentRatingTouched: true,
        libraryAccessTouched: true,
      }),
    ).toMatchObject({
      isChild: true,
      maxContentRating: "PG-13",
      libraryRestrictionsEnabled: true,
      allowedLibraryIDs: [9],
    });
  });

  it("clears access restrictions when the kids preset is turned off", () => {
    expect(
      clearKidsPreset(
        createProfileDraft(
          makeProfile({
            avatar: avatarPresetRef("dicebear:identicon:swift-fox"),
            is_child: true,
            max_content_rating: "PG",
            library_restrictions_enabled: true,
            allowed_library_ids: [2, 5],
            max_playback_quality: "1080p",
          }),
        ),
      ),
    ).toMatchObject({
      avatarPreset: "dicebear:identicon:swift-fox",
      isChild: false,
      maxContentRating: "",
      maxPlaybackQuality: "any",
      libraryRestrictionsEnabled: false,
      allowedLibraryIDs: [],
    });
  });
});
