import { describe, expect, it, vi } from "vitest";
import {
  connectWatchProviderAPIKey,
  fetchWatchProviders,
  pollWatchProviderDeviceAuth,
  startWatchProviderDeviceAuth,
  triggerWatchProviderSync,
  updateWatchProviderConnection,
} from "./watchProviders";

vi.mock("@/api/client", () => ({
  api: vi.fn(async (path: string, init?: RequestInit) => {
    if (path === "/watch-providers/trakt/auth/device-code") {
      return {
        ID: "auth-1",
        Provider: "trakt",
        UserCode: "31B677B9",
        VerificationURL: "https://trakt.tv/activate",
        IntervalSeconds: 5,
        ExpiresAt: "2026-05-04T15:57:08Z",
      };
    }
    if (path === "/watch-providers/trakt/sync") {
      return {
        run: {
          id: "run-1",
          connection_id: "conn-1",
          trigger: "manual",
          status: "running",
          provider: "trakt",
          inbound_watched_found: 0,
          inbound_watched_imported: 0,
          inbound_progress_found: 0,
          inbound_progress_imported: 0,
          outbound_found: 0,
          outbound_sent: 0,
          started_at: "2026-05-04T16:00:00Z",
          created_at: "2026-05-04T16:00:00Z",
        },
        retry_after_seconds: 0,
      };
    }
    return {
      path,
      method: init?.method ?? "GET",
      body: init?.body,
    };
  }),
}));

describe("watch provider queries", () => {
  it("uses the profile-scoped watch provider endpoints", async () => {
    await expect(fetchWatchProviders()).resolves.toMatchObject({ path: "/watch-providers" });
    await expect(startWatchProviderDeviceAuth("trakt")).resolves.toMatchObject({
      id: "auth-1",
      provider: "trakt",
      user_code: "31B677B9",
      verification_url: "https://trakt.tv/activate",
      interval_seconds: 5,
      expires_at: "2026-05-04T15:57:08Z",
    });
    await expect(pollWatchProviderDeviceAuth("trakt", "auth-1")).resolves.toMatchObject({
      path: "/watch-providers/trakt/auth/poll",
      method: "POST",
    });
    await expect(
      connectWatchProviderAPIKey("plugin:4:floppy", "token", {
        floppy: { base_url: "https://floppy.example.com" },
      }),
    ).resolves.toMatchObject({
      path: "/watch-providers/plugin:4:floppy/auth/api-key",
      method: "POST",
      body: JSON.stringify({
        api_key: "token",
        connection_config: { floppy: { base_url: "https://floppy.example.com" } },
      }),
    });
    await expect(
      updateWatchProviderConnection("trakt", { scrobble_enabled: true }),
    ).resolves.toMatchObject({
      path: "/watch-providers/trakt/connection",
      method: "PATCH",
    });
    await expect(triggerWatchProviderSync("trakt")).resolves.toMatchObject({
      run: {
        id: "run-1",
        status: "running",
      },
      retry_after_seconds: 0,
    });
  });
});
