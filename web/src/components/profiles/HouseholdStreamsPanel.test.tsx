// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import type { AdminSession } from "@/api/types";
import { buildWatchRouteRequest } from "@/pages/watchRouteHelpers";

import { HouseholdStreamsPanel } from "./HouseholdStreamsPanel";

const sessions: AdminSession[] = [];

vi.mock("@/hooks/queries/profiles", () => ({
  useHouseholdSessions: () => ({ data: sessions, isLoading: false, isFetching: false }),
}));

afterEach(() => {
  cleanup();
  sessions.length = 0;
});

describe("HouseholdStreamsPanel", () => {
  it("links to the version the profile is watching", () => {
    sessions.push({
      session_id: "session-1",
      profile_name: "Kid",
      content_id: "episode-1",
      media_file_id: 42,
      requested_media_file_id: 42,
      media_title: "Pilot",
      media_type: "episode",
      play_method: "direct",
      started_at: new Date().toISOString(),
      is_paused: false,
    } as AdminSession);

    render(
      <MemoryRouter>
        <HouseholdStreamsPanel />
      </MemoryRouter>,
    );

    const href = screen.getByRole("link").getAttribute("href") ?? "";
    const url = new URL(href, "http://localhost");
    expect(url.pathname).toBe("/watch/episode-1");
    expect(buildWatchRouteRequest("episode-1", url.searchParams).fileId).toBe(42);
  });
});
