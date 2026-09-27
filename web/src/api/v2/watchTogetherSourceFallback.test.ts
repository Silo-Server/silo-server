import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { fallbackRoomSource } from "./watchTogetherSourceFallback";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("host");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

const input = { selectionRevision: 3, failedFileId: 7, reason: "no_alternate_version" as const };
const response = () =>
  new Response(
    JSON.stringify({
      room_access_token: "proof",
      room: {
        room_id: "room",
        phase: "playing",
        playback_state: "waiting",
        selection_mode: "vote",
        guest_control_policy: "host_only",
        self_role: "guest",
        selection_revision: 4,
        generation: 9,
        selected_content_id: "movie",
        selected_file_id: "8",
        anchor_position_seconds: 900,
        anchor_updated_at: "2026-01-01T00:00:00Z",
      },
    }),
    { headers: { "Content-Type": "application/json" } },
  );

it("sends the failed file, selection revision, reason, and room proof once", async () => {
  const fetch = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetch);
  const result = await fallbackRoomSource("room", "proof", input);
  expect(result.room.selected_file_id).toBe(8);
  expect(result.room.anchor_position_seconds).toBe(900);
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room/source-fallback");
  expect(JSON.parse(fetch.mock.calls[0]![1].body)).toEqual({
    selection_revision: 3,
    failed_file_id: "7",
    reason: "no_alternate_version",
  });
  expect(new Headers(fetch.mock.calls[0]![1].headers).get("X-Room-Token")).toBe("proof");
});

it.each([401, 403, 409, 422, 500])(
  "does not loop or change the request after a %s refusal",
  async (status) => {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await expect(fallbackRoomSource("room", "proof", input)).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
  },
);

it("rejects a response after the active profile changes", async () => {
  let resolve!: (response: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((done) => {
          resolve = done;
        }),
    ),
  );
  const pending = fallbackRoomSource("room", "proof", input);
  setProfileId("guest");
  resolve(response());
  await expect(pending).rejects.toThrow();
});
