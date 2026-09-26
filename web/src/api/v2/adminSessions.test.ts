import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import { captureAdminUserAuthority } from "./adminUsers";
import { listAdminPlaybackSessions } from "./adminSessions";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("admin");
  setProfileId("primary");
});
afterEach(() => vi.unstubAllGlobals());
const row = {
  session_id: "s",
  user_id: "7",
  profile_id: "child",
  media_file_id: "42",
  requested_media_file_id: "41",
  routing_execution_node_id: "9",
  target_audio_channels: 2,
  source_audio_channels: 8,
};
it("drains empty continuation pages and retains profile, source and audio distinctions", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      jsonResponse({ items: [], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockResolvedValueOnce(jsonResponse({ items: [row], page: { has_more: false } }));
  vi.stubGlobal("fetch", fetch);
  const result = await listAdminPlaybackSessions(captureAdminUserAuthority());
  expect(result).toEqual([
    {
      ...row,
      user_id: 7,
      media_file_id: 42,
      requested_media_file_id: 41,
      routing_execution_node_id: 9,
      routing_egress_node_id: undefined,
    },
  ]);
  expect(String(fetch.mock.calls[0]?.[0])).toContain("/api/v2/admin/sessions");
  expect(String(fetch.mock.calls[1]?.[0])).toContain("cursor=next");
});
it("fails repeated cursors without publishing a partial list", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockImplementation(async () =>
        jsonResponse({ items: [row], page: { has_more: true, next_cursor: "same" } }),
      ),
  );
  await expect(listAdminPlaybackSessions(captureAdminUserAuthority())).rejects.toThrow(
    "continuation",
  );
});
it("rejects numeric IDs that would lose identity precision", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      jsonResponse({
        items: [{ ...row, media_file_id: "9007199254740993" }],
        page: { has_more: false },
      }),
    ),
  );
  await expect(listAdminPlaybackSessions(captureAdminUserAuthority())).rejects.toThrow(
    "identifier",
  );
});
it("discards the completed page after a profile switch", async () => {
  let finish!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    ),
  );
  const result = listAdminPlaybackSessions(captureAdminUserAuthority());
  setProfileId("other");
  finish(jsonResponse({ items: [row], page: { has_more: false } }));
  await expect(result).rejects.toThrow();
});
