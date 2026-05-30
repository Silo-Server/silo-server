// @vitest-environment node

import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LibraryPlaybackPreference, Profile, UserLibrary } from "@/api/types";
import {
  buildLibraryPlaybackSummary,
  buildLibraryPlaybackRequest,
  createLibraryPlaybackEditorState,
} from "./libraryPlaybackPreferences";

const mocks = vi.hoisted(() => ({
  useAvailableUserLibraries: vi.fn(),
  useCurrentProfile: vi.fn(),
  useLibraryPlaybackPreferences: vi.fn(),
  useDeleteDeviceSetting: vi.fn(),
  useEffectiveSettings: vi.fn(),
  useSetting: vi.fn(),
  useSetDeviceSetting: vi.fn(),
  useSetSetting: vi.fn(),
}));

vi.mock("@/hooks/queries/libraries", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  );

  return {
    ...actual,
    useAvailableUserLibraries: (...args: unknown[]) => mocks.useAvailableUserLibraries(...args),
  };
});

vi.mock("@/hooks/queries/settings", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/settings")>(
    "@/hooks/queries/settings",
  );

  return {
    ...actual,
    useDeleteDeviceSetting: (...args: unknown[]) => mocks.useDeleteDeviceSetting(...args),
    useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
    useSetting: (...args: unknown[]) => mocks.useSetting(...args),
    useSetDeviceSetting: (...args: unknown[]) => mocks.useSetDeviceSetting(...args),
    useSetSetting: (...args: unknown[]) => mocks.useSetSetting(...args),
  };
});

vi.mock("@/hooks/queries/libraryPlaybackPreferences", () => ({
  useLibraryPlaybackPreferences: (...args: unknown[]) =>
    mocks.useLibraryPlaybackPreferences(...args),
  useSetLibraryPlaybackPreference: () => ({
    isPending: false,
    mutate: vi.fn(),
  }),
  useDeleteLibraryPlaybackPreference: () => ({
    isPending: false,
    mutate: vi.fn(),
  }),
}));

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

function makePreference(
  overrides: Partial<LibraryPlaybackPreference> = {},
): LibraryPlaybackPreference {
  return {
    profile_id: "profile-1",
    library_id: overrides.library_id ?? 7,
    updated_at: overrides.updated_at ?? "2026-03-23T00:00:00Z",
    ...overrides,
  };
}

describe("LibrarySettings helpers", () => {
  it("summarizes inherited rows as using profile defaults", () => {
    expect(buildLibraryPlaybackSummary(null)).toBe("Uses profile defaults");
  });

  it("summarizes only the overridden playback fields", () => {
    expect(
      buildLibraryPlaybackSummary(
        makePreference({
          audio_language: "ja",
          subtitle_language: "en",
          subtitle_mode: "always",
          show_forced_subtitles: false,
        }),
      ),
    ).toBe("Audio: Japanese • Subtitles: English • Behavior: Always • Forced subtitles: Off");
  });

  it("summarizes original audio language overrides with the original language label", () => {
    expect(
      buildLibraryPlaybackSummary(
        makePreference({
          audio_language: "original",
        }),
      ),
    ).toBe("Audio: Original Language");
  });

  it("initializes editor state with inherit values when a library has no override", () => {
    expect(createLibraryPlaybackEditorState(null)).toEqual({
      audioLanguage: "inherit",
      subtitleLanguage: "inherit",
      subtitleMode: "inherit",
      showForcedSubtitles: "inherit",
    });
  });

  it("maps stored empty subtitle overrides back to the none option", () => {
    expect(
      createLibraryPlaybackEditorState(
        makePreference({
          subtitle_language: "",
        }),
      ),
    ).toEqual({
      audioLanguage: "inherit",
      subtitleLanguage: "none",
      subtitleMode: "inherit",
      showForcedSubtitles: "inherit",
    });
  });

  it("keeps inherited fields out of the request payload and serializes none as an empty string", () => {
    expect(
      buildLibraryPlaybackRequest({
        audioLanguage: "inherit",
        subtitleLanguage: "none",
        subtitleMode: "inherit",
        showForcedSubtitles: "off",
      }),
    ).toEqual({
      subtitle_language: "",
      show_forced_subtitles: false,
    });
  });

  it("treats an omitted subtitle field as inherited instead of none", () => {
    expect(
      createLibraryPlaybackEditorState(
        makePreference({
          subtitle_language: undefined,
        }),
      ),
    ).toEqual({
      audioLanguage: "inherit",
      subtitleLanguage: "inherit",
      subtitleMode: "inherit",
      showForcedSubtitles: "inherit",
    });
  });
});

describe("LibrarySettings", () => {
  beforeEach(() => {
    mocks.useAvailableUserLibraries.mockReset();
    mocks.useCurrentProfile.mockReset();
    mocks.useLibraryPlaybackPreferences.mockReset();
    mocks.useDeleteDeviceSetting.mockReset();
    mocks.useEffectiveSettings.mockReset();
    mocks.useSetting.mockReset();
    mocks.useSetDeviceSetting.mockReset();
    mocks.useSetSetting.mockReset();

    mocks.useAvailableUserLibraries.mockReturnValue({
      data: libraries,
      isLoading: false,
    });
    mocks.useCurrentProfile.mockReturnValue({
      profile,
      isLoading: false,
    });
    mocks.useLibraryPlaybackPreferences.mockReturnValue({
      data: [],
      isLoading: false,
    });
    mocks.useDeleteDeviceSetting.mockReturnValue({
      isPending: false,
      mutateAsync: vi.fn(),
    });
    mocks.useEffectiveSettings.mockReturnValue({
      data: {},
      isLoading: false,
    });
    mocks.useSetting.mockReturnValue({
      data: null,
      isLoading: false,
    });
    mocks.useSetDeviceSetting.mockReturnValue({
      isPending: false,
      mutateAsync: vi.fn(),
    });
    mocks.useSetSetting.mockReturnValue({
      isPending: false,
      mutate: vi.fn(),
    });
  });

  it("renders the inherited summary for libraries without playback overrides", () => {
    const markup = renderToStaticMarkup(<LibrarySettings />);

    expect(markup).toContain("Uses profile defaults");
    expect(markup).toContain("Remember library pages");
    expect(markup).toContain("Edit playback overrides");
  });

  it("renders the playback override summary for libraries with saved defaults", () => {
    mocks.useLibraryPlaybackPreferences.mockReturnValue({
      data: [
        makePreference({
          library_id: 7,
          audio_language: "ja",
          subtitle_language: "en",
          subtitle_mode: "always",
          show_forced_subtitles: false,
        }),
      ],
      isLoading: false,
    });

    const markup = renderToStaticMarkup(<LibrarySettings />);

    expect(markup).toContain(
      "Audio: Japanese • Subtitles: English • Behavior: Always • Forced subtitles: Off",
    );
  });
});
