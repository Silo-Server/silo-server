import { act, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LoginResponse, Profile } from "@/api/types";
import { v2Fixture } from "@/api/v2/testing";
import listAuthProvidersOk from "../../../contracts/api/v2/fixtures/list_auth_providers_ok.json";
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
const refreshAuthenticationMock = vi.hoisted(() => vi.fn());
const v2Mock = vi.hoisted(() => vi.fn());

vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");

  return {
    ...actual,
    api: apiMock,
    bootstrapAccessToken: bootstrapAccessTokenMock,
    getAccessToken: getAccessTokenMock,
    onProfileUnverified: onProfileUnverifiedMock,
    refreshAuthentication: refreshAuthenticationMock,
    setAccessToken: setAccessTokenMock,
    setProfileId: setProfileIdMock,
    setProfileToken: setProfileTokenMock,
    setRefreshToken: setRefreshTokenMock,
  };
});

vi.mock("@/api/v2/account", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/account")>("@/api/v2/account");
  return { ...actual, restoreUserSession: restoreUserSessionMock };
});

vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
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

function makeSession(id: number, username: string): LoginResponse {
  return {
    access_token: `access-${id}`,
    refresh_token: `refresh-${id}`,
    expires_in: 3600,
    user: {
      id,
      username,
      email: "",
      role: "user",
      permissions: [],
      download_allowed: false,
      impersonation: null,
    },
  };
}

function AccountProbe() {
  const { user, loading, completeLogin } = useAuth();

  return (
    <div>
      <div data-testid="signed-in-user">{loading ? "loading" : (user?.username ?? "none")}</div>
      <button onClick={() => completeLogin(makeSession(1, "laura"))}>Sign in as laura</button>
      <button onClick={() => completeLogin(makeSession(2, "sam"))}>Sign in as sam</button>
    </div>
  );
}

function TemporaryPasswordProbe() {
  const { user, pendingPasswordChange, loading, completeLogin, settleTemporaryPassword } =
    useAuth();
  const temporary = makeSession(1, "laura");
  temporary.user.password_change_required = true;
  return (
    <div>
      <div data-testid="signed-in-user">{loading ? "loading" : (user?.username ?? "none")}</div>
      <div data-testid="pending-user">{pendingPasswordChange?.username ?? "none"}</div>
      <button onClick={() => completeLogin(temporary)}>Sign in with a temporary password</button>
      <button onClick={() => void settleTemporaryPassword()}>Settle</button>
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
    v2Mock.mockImplementation((key: string) => {
      if (key === "GET /api/v2/system/setup") {
        return Promise.resolve(
          v2Fixture<"GET /api/v2/system/setup">({ needs_setup: false, wizard_completed: false }),
        );
      }
      if (key === "GET /api/v2/auth/providers") {
        return Promise.resolve(v2Fixture<"GET /api/v2/auth/providers">({ items: [] }));
      }
      return Promise.reject(new Error(`unexpected v2 call: ${key}`));
    });
  });

  it("preserves OAuth login providers returned by the auth providers endpoint", async () => {
    v2Mock.mockImplementation((key: string) => {
      if (key === "GET /api/v2/system/setup") {
        return Promise.resolve(
          v2Fixture<"GET /api/v2/system/setup">({ needs_setup: false, wizard_completed: false }),
        );
      }
      if (key === "GET /api/v2/auth/providers") {
        return Promise.resolve(v2Fixture<"GET /api/v2/auth/providers">(listAuthProvidersOk));
      }
      return Promise.reject(new Error(`unexpected v2 call: ${key}`));
    });
    apiMock.mockImplementation((path: string) =>
      Promise.reject(new Error(`unexpected API call: ${path}`)),
    );

    renderWithAuthProvider(<ProviderProbe />);

    await waitFor(() => {
      expect(screen.getByTestId("providers")).toHaveTextContent("local:credentials");
      expect(screen.getByTestId("providers")).toHaveTextContent("plugin-3:oauth");
    });
  });

  it("clears cached profile-scoped data when the active profile changes", async () => {
    apiMock.mockImplementation((path: string) =>
      Promise.reject(new Error(`unexpected API call: ${path}`)),
    );

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
    apiMock.mockImplementation((path: string) =>
      Promise.reject(new Error(`unexpected API call: ${path}`)),
    );

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

  it("keeps a temporary-password session signed out until the password is changed", async () => {
    renderWithAuthProvider(<TemporaryPasswordProbe />);
    await waitFor(() => expect(screen.getByTestId("signed-in-user")).toHaveTextContent("none"));

    await act(async () => {
      screen.getByRole("button", { name: "Sign in with a temporary password" }).click();
    });
    // Nothing keyed on `user` runs for the restricted session, including the
    // sole-profile bootstrap.
    expect(screen.getByTestId("signed-in-user")).toHaveTextContent("none");
    expect(screen.getByTestId("pending-user")).toHaveTextContent("laura");
    expect(v2Mock.mock.calls.some(([key]) => String(key).includes("/profiles"))).toBe(false);

    refreshAuthenticationMock.mockResolvedValue(true);
    v2Mock.mockImplementation((key: string) =>
      key === "GET /api/v2/account/me"
        ? Promise.resolve(
            v2Fixture<"GET /api/v2/account/me">({
              id: "1",
              username: "laura",
              email: "",
              role: "user",
              permissions: [],
              download_allowed: false,
              password_change_required: false,
            }),
          )
        : Promise.reject(new Error(`unexpected v2 call: ${key}`)),
    );
    await act(async () => {
      screen.getByRole("button", { name: "Settle" }).click();
    });
    await waitFor(() => expect(screen.getByTestId("signed-in-user")).toHaveTextContent("laura"));
    expect(screen.getByTestId("pending-user")).toHaveTextContent("none");
    expect(refreshAuthenticationMock).toHaveBeenCalledTimes(1);
  });

  it("clears the cache before a different account replaces the signed-in one", async () => {
    renderWithAuthProvider(<AccountProbe />);
    await waitFor(() => expect(screen.getByTestId("signed-in-user")).toHaveTextContent("none"));

    // Which account was on screen each time the cache was cleared.
    const shownAtClear: Array<string | null> = [];
    queryClientClearMock.mockImplementation(() => {
      shownAtClear.push(screen.getByTestId("signed-in-user").textContent);
    });

    await act(async () => {
      screen.getByRole("button", { name: "Sign in as laura" }).click();
    });
    await act(async () => {
      screen.getByRole("button", { name: "Sign in as laura" }).click();
    });
    expect(screen.getByTestId("signed-in-user")).toHaveTextContent("laura");
    // Signing in from signed out, or again as the same account, keeps the cache.
    expect(shownAtClear).toEqual([]);

    await act(async () => {
      screen.getByRole("button", { name: "Sign in as sam" }).click();
    });
    expect(screen.getByTestId("signed-in-user")).toHaveTextContent("sam");
    // Cleared while laura was still rendered: none of sam's reads had started.
    expect(shownAtClear).toEqual(["laura"]);
  });
});
