// @vitest-environment node

import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Profile, UserLibrary } from "@/api/types";
import type { EffectiveSetting, EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import {
  buildLibraryPlaybackMutations,
  buildLibraryPlaybackSummaryFromState,
  createLibraryPlaybackEditorState,
} from "./libraryPlaybackPreferences";

const mocks = vi.hoisted(() => ({
  useAvailableUserLibraries: vi.fn(),
  useLibraryDisplayPreferences: vi.fn(),
  useCurrentProfile: vi.fn(),
  useEffectiveSettings: vi.fn(),
  useSetSettingValue: vi.fn(),
  useClearSettingValue: vi.fn(),
}));

vi.mock("@/hooks/queries/libraries", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  );

  return {
    ...actual,
    useAvailableUserLibraries: (...args: unknown[]) => mocks.useAvailableUserLibraries(...args),
    useLibraryDisplayPreferences: (...args: unknown[]) =>
      mocks.useLibraryDisplayPreferences(...args),
  };
});

vi.mock("@/hooks/queries/settingValues", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/settingValues")>(
    "@/hooks/queries/settingValues",
  );

  return {
    ...actual,
    useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
    useSetSettingValue: (...args: unknown[]) => mocks.useSetSettingValue(...args),
    useClearSettingValue: (...args: unknown[]) => mocks.useClearSettingValue(...args),
  };
});

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: (...args: unknown[]) => mocks.useCurrentProfile(...args),
}));

import LibrarySettings from "./LibrarySettings";

const profile = {
  id: "profile-1",
  name: "Main",
  avatar: "avatar-1",
  has_pin: false,
  is_child: false,
  is_primary: true,
  max_content_rating: "pg-13",
  quality_preference: "auto",
  language: "en",
  subtitle_language: "",
  subtitle_mode: "auto",
  show_forced_subtitles: true,
  auto_skip_intro: false,
  auto_skip_credits: false,
  library_restrictions_enabled: false,
  allowed_library_ids: null,
  max_playback_quality: "4k",
  created_at: "2026-03-23T00:00:00Z",
  updated_at: "2026-03-23T00:00:00Z",
} satisfies Profile;

const libraries: UserLibrary[] = [
  { id: 7, name: "Anime", type: "series", sort_order: 0 },
  { id: 9, name: "Movies", type: "movies", sort_order: 1 },
];

/** One entry of a resolved effective-settings map. */
function resolved(
  key: SettingKey,
  value: unknown,
  source: EffectiveSetting["source"],
  extra: Partial<EffectiveSetting> = {},
): EffectiveSettingsMap {
  return { [key]: { key, value, source, ...extra } };
}

describe("library playback editor state", () => {
  it("reads inherit for every key resolved above the library", () => {
    expect(
      createLibraryPlaybackEditorState({
        ...resolved(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, "en", "profile"),
        ...resolved(SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, "auto", "default"),
      }),
    ).toEqual({
      audioLanguage: "inherit",
      subtitleLanguage: "inherit",
      subtitleMode: "inherit",
      showForcedSubtitles: "inherit",
    });
  });

  it("reads an override only from a value the library itself holds", () => {
    expect(
      createLibraryPlaybackEditorState({
        // Same value as the profile default, but stored on the library: still
        // an override, which is why source rather than value decides.
        ...resolved(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, "ja", "profile_library", {
          library_id: 7,
        }),
        ...resolved(SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, "always", "profile_library", {
          library_id: 7,
        }),
        ...resolved(SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES, false, "profile_library", {
          library_id: 7,
        }),
      }),
    ).toEqual({
      audioLanguage: "ja",
      subtitleLanguage: "inherit",
      subtitleMode: "always",
      showForcedSubtitles: "off",
    });
  });

  it("distinguishes a stored null from an absent subtitle language", () => {
    // A library row holding null is an explicit "no subtitles here", which must
    // not read as the inherit the absence of a row means.
    expect(
      createLibraryPlaybackEditorState(
        resolved(SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE, null, "profile_library", {
          library_id: 7,
        }),
      ).subtitleLanguage,
    ).toBe("none");
    expect(createLibraryPlaybackEditorState({}).subtitleLanguage).toBe("inherit");
  });

  it("summarizes an untouched library as using profile defaults", () => {
    expect(buildLibraryPlaybackSummaryFromState(createLibraryPlaybackEditorState({}))).toBe(
      "Uses profile defaults",
    );
  });

  it("summarizes only the overridden playback fields", () => {
    expect(
      buildLibraryPlaybackSummaryFromState({
        audioLanguage: "ja",
        subtitleLanguage: "en",
        subtitleMode: "always",
        showForcedSubtitles: "off",
      }),
    ).toBe("Audio: Japanese • Subtitles: English • Behavior: Always on • Forced subtitles: Off");
  });
});

describe("library playback mutations", () => {
  it("plans a clear for every inherited field and a typed value for the rest", () => {
    expect(
      buildLibraryPlaybackMutations({
        audioLanguage: "inherit",
        subtitleLanguage: "none",
        subtitleMode: "inherit",
        showForcedSubtitles: "off",
      }),
    ).toEqual([
      // No value means "delete the row at this scope so it inherits again".
      { key: SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE },
      { key: SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE, value: null },
      { key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE },
      { key: SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES, value: false },
    ]);
  });

  it("sends a language tag rather than the legacy empty string for a chosen language", () => {
    expect(
      buildLibraryPlaybackMutations({
        audioLanguage: "ja",
        subtitleLanguage: "inherit",
        subtitleMode: "always",
        showForcedSubtitles: "inherit",
      }),
    ).toEqual([
      { key: SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, value: "ja" },
      { key: SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE },
      { key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, value: "always" },
      { key: SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES },
    ]);
  });
});

describe("LibrarySettings", () => {
  beforeEach(() => {
    mocks.useAvailableUserLibraries.mockReset();
    mocks.useLibraryDisplayPreferences.mockReset();
    mocks.useCurrentProfile.mockReset();
    mocks.useEffectiveSettings.mockReset();
    mocks.useSetSettingValue.mockReset();
    mocks.useClearSettingValue.mockReset();

    mocks.useAvailableUserLibraries.mockReturnValue({
      data: libraries,
      isLoading: false,
    });
    mocks.useLibraryDisplayPreferences.mockReturnValue({
      disabledLibraryIDs: [],
      libraryOrder: [],
      isLoading: false,
    });
    mocks.useCurrentProfile.mockReturnValue({
      profile,
      isLoading: false,
    });
    mocks.useEffectiveSettings.mockReturnValue({
      data: {},
      isLoading: false,
    });
    mocks.useSetSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: vi.fn(),
    });
    mocks.useClearSettingValue.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
      mutateAsync: vi.fn(),
    });
  });

  it("renders the inherited summary for libraries without playback overrides", () => {
    const markup = renderToStaticMarkup(<LibrarySettings />);

    expect(markup).toContain("Uses profile defaults");
    expect(markup).toContain("Remember library pages");
    expect(markup).toContain("Edit playback overrides");
  });

  it("renders the playback override summary from the values resolved for that library", () => {
    mocks.useEffectiveSettings.mockImplementation(
      (options?: { libraryIds?: readonly number[] }) => {
        if (!options?.libraryIds?.length) {
          return { data: {}, isLoading: false };
        }
        return {
          isLoading: false,
          data: {
            ...resolved(SETTING_KEYS.PLAYBACK_AUDIO_LANGUAGE, "ja", "profile_library", {
              library_id: options.libraryIds[0],
            }),
            ...resolved(SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE, "en", "profile_library", {
              library_id: options.libraryIds[0],
            }),
            ...resolved(SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, "always", "profile_library", {
              library_id: options.libraryIds[0],
            }),
            ...resolved(SETTING_KEYS.PLAYBACK_SHOW_FORCED_SUBTITLES, false, "profile_library", {
              library_id: options.libraryIds[0],
            }),
          },
        };
      },
    );

    const markup = renderToStaticMarkup(<LibrarySettings />);

    expect(markup).toContain(
      "Audio: Japanese • Subtitles: English • Behavior: Always on • Forced subtitles: Off",
    );
  });

  it("resolves each library's overrides with that library in context", () => {
    renderToStaticMarkup(<LibrarySettings />);

    // Every card must resolve for its own library, or one library's override
    // would be reported for another.
    for (const library of libraries) {
      expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
        expect.objectContaining({ libraryIds: [library.id] }),
      );
    }
  });
});
