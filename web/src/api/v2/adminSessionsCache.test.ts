import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminSession } from "@/api/types";
import { adminSessionsKey, applyAdminSessionPauseEvent } from "./adminSessionsCache";

const authority: ProfileRequestContextSnapshot = {
  accessToken: "token",
  authContextVersion: 1,
  serverOrigin: "https://silo.test",
  profileId: "profile-1",
  profileToken: null,
};

function row(id: string, isPaused: boolean, lifecycleSeq?: number): AdminSession {
  return { session_id: id, is_paused: isPaused, lifecycle_seq: lifecycleSeq } as AdminSession;
}

function cached(client: QueryClient) {
  return client.getQueryData<AdminSession[]>(adminSessionsKey(authority));
}

describe("applyAdminSessionPauseEvent", () => {
  it("shows a pause or resume event at once, ordered by lifecycle number", () => {
    const client = new QueryClient();
    client.setQueryData(adminSessionsKey(authority), [row("a", false, 4), row("b", false)]);

    expect(
      applyAdminSessionPauseEvent(client, authority, { session_id: "a", seq: 7, state: "paused" }),
    ).toBe(true);
    expect(cached(client)?.[0]).toMatchObject({ is_paused: true, lifecycle_seq: 7 });

    // An event older than the row the list already holds changes nothing.
    expect(
      applyAdminSessionPauseEvent(client, authority, { session_id: "a", seq: 5, state: "playing" }),
    ).toBe(false);
    expect(cached(client)?.[0]).toMatchObject({ is_paused: true, lifecycle_seq: 7 });

    // A row read before lifecycle numbers existed takes any event.
    applyAdminSessionPauseEvent(client, authority, { session_id: "b", seq: 2, state: "paused" });
    expect(cached(client)?.[1]).toMatchObject({ is_paused: true, lifecycle_seq: 2 });

    // A session the list does not hold, or a malformed frame, is left to the re-read.
    expect(
      applyAdminSessionPauseEvent(client, authority, { session_id: "c", seq: 9, state: "paused" }),
    ).toBe(false);
    expect(applyAdminSessionPauseEvent(client, authority, null)).toBe(false);
    expect(cached(client)).toHaveLength(2);
  });
});
