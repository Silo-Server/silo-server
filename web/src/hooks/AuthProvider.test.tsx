import { act, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AuthProviderOption, Profile, SetupStatusResponse } from "@/api/types";
import { storage } from "@/utils/storage";
import { AuthProvider, useAuth } from "./useAuth";

const apiMock = vi.hoisted(() => vi.fn());
const bootstrapAccessTokenMock = vi.hoisted(() => vi.fn());
const getAccessTokenMock = vi.hoisted(() => vi.fn());
const onProfileUnverifiedMock = vi.hoisted(() => vi.fn());
const restoreUserSessionMock = vi.hoisted(() => vi.fn());
const setAccessTokenMock = vi.hoisted(() => vi.fn());
const setProfileIdMock = vi.hoisted(() => vi.fn());
const setProfileTokenMock = vi.hoisted(() => vi.fn());
const setRefreshTokenMock = vi.hoisted(() => vi.fn());
const queryClientClearMock = vi.hoisted(() => vi.fn());

vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");

  return {
    ...actual,
    api: apiMock,
    bootstrapAccessToken: bootstrapAccessTokenMock,
    getAccessToken: getAccessTokenMock,
    onProfileUnverified: onProfileUnverifiedMock,
    restoreUserSession: restoreUserSessionMock,
    setAccessToken: setAccessTokenMock,
    setProfileId: setProfileIdMock,
    setProfileToken: setProfileTokenMock,
    setRefreshToken: setRefreshTokenMock,
  };
});

vi.mock("@/lib/query-client", () => ({
  queryClient: {
    clear: queryClientClearMock,
  },
}));

function makeProfile(id: string, name: string): Profile {
  return {
    id,
    name,
    avatar: "",
    has_pin: false,
    is_child: false,
    is_primary: id === "profile-1",
    max_content_rating: "",
    quality_preference: "1080p",
    language: "en",
    subtitle_language: "",
    subtitle_mode: "auto",
    show_forced_subtitles: true,
    auto_skip_intro: false,
    auto_skip_credits: false,
    library_restrictions_enabled: false,
    allowed_library_ids: [],
    max_playback_quality: "",
    created_at: "2024-01-01T00:00:00Z",
    updated_at: "2024-01-01T00:00:00Z",
  };
}

function renderWithAuthProvider(children: ReactNode) {
  return render(<AuthProvider>{children}</AuthProvider>);
}

function ProviderProbe() {
  const { providers, setupLoading } = useAuth();

  return (
    <div data-testid="providers">
      {setupLoading ? "loading" : providers.map((entry) => `${entry.id}:${entry.mode}`).join(",")}
    </div>
  );
}

function ProfileSelectionProbe() {
  const { profile, selectProfile } = useAuth();
  const profileOne = makeProfile("profile-1", "Alex");
  const renamedProfileOne = makeProfile("profile-1", "Alex Updated");
  const profileTwo = makeProfile("profile-2", "Sam");

  return (
    <div>
      <div data-testid="active-profile">{profile?.name ?? "none"}</div>
      <button onClick={() => selectProfile(profileOne)}>Select profile one</button>
      <button onClick={() => selectProfile(renamedProfileOne)}>Update profile one</button>
      <button onClick={() => selectProfile(profileTwo)}>Select profile two</button>
    </div>
  );
}

describe("AuthProvider", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    Object.values(storage.KEYS).forEach((key) => storage.remove(key));

    bootstrapAccessTokenMock.mockResolvedValue(false);
    getAccessTokenMock.mockReturnValue(null);
    restoreUserSessionMock.mockResolvedValue(null);
  });

  it("preserves OAuth login providers returned by the auth providers endpoint", async () => {
    const providers: AuthProviderOption[] = [
      { id: "local", display_name: "Local", mode: "credentials", default: true },
      {
        id: "plugin:41:oidc",
        display_name: "OIDC",
        mode: "oauth",
        default: false,
        installation_id: 41,
      },
    ];

    apiMock.mockImplementation(
      (path: string): Promise<SetupStatusResponse | AuthProviderOption[]> => {
        if (path === "/auth/setup") {
          return Promise.resolve({ needs_setup: false });
        }
        if (path === "/auth/providers") {
          return Promise.resolve(providers);
        }
        return Promise.reject(new Error(`unexpected API call: ${path}`));
      },
    );

    renderWithAuthProvider(<ProviderProbe />);

    await waitFor(() => {
      expect(screen.getByTestId("providers")).toHaveTextContent("local:credentials");
      expect(screen.getByTestId("providers")).toHaveTextContent("plugin:41:oidc:oauth");
    });
  });

  it("clears cached profile-scoped data when the active profile changes", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path === "/auth/setup") {
        return Promise.resolve({ needs_setup: false });
      }
      if (path === "/auth/providers") {
        return Promise.resolve([]);
      }
      return Promise.reject(new Error(`unexpected API call: ${path}`));
    });

    renderWithAuthProvider(<ProfileSelectionProbe />);

    await act(async () => {
      screen.getByRole("button", { name: "Select profile one" }).click();
    });
    expect(screen.getByTestId("active-profile")).toHaveTextContent("Alex");

    queryClientClearMock.mockClear();
    await act(async () => {
      screen.getByRole("button", { name: "Select profile two" }).click();
    });

    expect(queryClientClearMock).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("active-profile")).toHaveTextContent("Sam");
  });

  it("keeps cached data when updating the active profile without changing identity", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path === "/auth/setup") {
        return Promise.resolve({ needs_setup: false });
      }
      if (path === "/auth/providers") {
        return Promise.resolve([]);
      }
      return Promise.reject(new Error(`unexpected API call: ${path}`));
    });

    renderWithAuthProvider(<ProfileSelectionProbe />);

    await act(async () => {
      screen.getByRole("button", { name: "Select profile one" }).click();
    });
    queryClientClearMock.mockClear();

    await act(async () => {
      screen.getByRole("button", { name: "Update profile one" }).click();
    });

    expect(queryClientClearMock).not.toHaveBeenCalled();
    expect(screen.getByTestId("active-profile")).toHaveTextContent("Alex Updated");
  });
});
