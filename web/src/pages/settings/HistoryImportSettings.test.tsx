import { describe, expect, it } from "vitest";

import type { EmbyConnectLoginResponse, HistoryImportSource } from "@/api/types";
import { canStartEmbyImport, canStartJellyfinImport } from "./HistoryImportSettings.utils";

describe("HistoryImportSettings helpers", () => {
  it("only allows imports when the selected mode has the required inputs", () => {
    const connectSession = {
      connect_session_id: "connect-session-1",
      servers: [{ server_id: "server-1", name: "Main" }],
    } as EmbyConnectLoginResponse;
    const savedSource = { id: 7, name: "Quickflix" } as HistoryImportSource;

    expect(
      canStartEmbyImport("connect", "profile-1", connectSession, "server-1", undefined, ""),
    ).toBe(true);
    expect(canStartEmbyImport("connect", "profile-1", null, "server-1", undefined, "")).toBe(false);
    expect(canStartEmbyImport("connect", "", connectSession, "server-1", undefined, "")).toBe(
      false,
    );
    // Emby accounts may have no password; the username is still required.
    expect(canStartEmbyImport("saved", "profile-1", null, "", savedSource, "kid")).toBe(true);
    expect(canStartEmbyImport("saved", "profile-1", null, "", savedSource, "  ")).toBe(false);
    expect(canStartEmbyImport("saved", "profile-1", null, "", undefined, "kid")).toBe(false);
  });

  it("requires Jellyfin manual imports to have a profile, server URL, username, and password", () => {
    expect(canStartJellyfinImport("profile-1", "https://jellyfin.example", "alice", "secret")).toBe(
      true,
    );
    expect(canStartJellyfinImport("", "https://jellyfin.example", "alice", "secret")).toBe(false);
    expect(canStartJellyfinImport("profile-1", "", "alice", "secret")).toBe(false);
    expect(canStartJellyfinImport("profile-1", "https://jellyfin.example", "", "secret")).toBe(
      false,
    );
    expect(canStartJellyfinImport("profile-1", "https://jellyfin.example", "alice", "")).toBe(
      false,
    );
  });
});
