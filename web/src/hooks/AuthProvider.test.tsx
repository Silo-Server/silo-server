import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AuthProviderOption, SetupStatusResponse } from "@/api/types";
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
});
