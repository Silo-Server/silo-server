import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureAdminUserAuthority, type AdminUserEditor } from "@/api/v2/adminUsers";
import { AdminUserDeleteDialog } from "./AdminUserDeleteDialog";
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("retains delete confirmation after412 and uses only explicitly reloaded validator", async () => {
  const user = {
    id: "7",
    username: "Target",
    email: "target@example.invalid",
    role: "user",
    permissions: [],
    enabled: true,
    library_ids: null,
    access_group_id: null,
    max_playback_quality: null,
    max_streams: null,
    max_transcodes: null,
    max_remote_stream_bitrate_kbps: null,
    max_local_stream_bitrate_kbps: null,
    transcode_allowed: null,
    audio_transcode_allowed: null,
    max_profiles: 5,
    download_allowed: null,
    download_transcode_allowed: null,
    requests_allowed: null,
    effective_policy: {
      library_ids: [],
      max_playback_quality: "",
      max_streams: 0,
      max_transcodes: 0,
      max_remote_stream_bitrate_kbps: 0,
      max_local_stream_bitrate_kbps: 0,
      transcode_allowed: true,
      audio_transcode_allowed: true,
      download_allowed: true,
      download_transcode_allowed: true,
      requests_allowed: true,
      permissions: [],
    },
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    last_active_at: null,
  };
  const initialEditor = {
    user: { ...user, id: 7, last_active_at: undefined },
    etag: '"old"',
    profileContext: captureAdminUserAuthority(),
  } as AdminUserEditor;
  const done = vi.fn();
  const close = vi.fn();
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          type: "https://silo.example/problems/precondition_failed",
          title: "Changed",
          status: 412,
          detail: "Reload",
        }),
        { status: 412, headers: { "Content-Type": "application/problem+json", ETag: '"ignored"' } },
      ),
    )
    .mockResolvedValueOnce(
      new Response(JSON.stringify(user), {
        headers: { "Content-Type": "application/json", ETag: '"fresh"' },
      }),
    )
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AdminUserDeleteDialog initialEditor={initialEditor} onClose={close} onDeleted={done} />
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await screen.findByText("The user changed. Reload before trying again.");
  expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
  expect(close).not.toHaveBeenCalled();
  expect(fetch).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload current user" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(done).toHaveBeenCalledTimes(1));
  expect(new Headers(fetch.mock.calls[0]![1]?.headers).get("If-Match")).toBe('"old"');
  expect(new Headers(fetch.mock.calls[2]![1]?.headers).get("If-Match")).toBe('"fresh"');
});
