// @vitest-environment jsdom
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
  max_profiles: null,
  download_allowed: null,
  download_transcode_allowed: null,
  requests_allowed: null,
  password_login: true,
  password_change_required: false,
  is_owner: false,
  break_glass: false,
  effective_policy: {
    library_ids: [],
    max_playback_quality: "",
    max_streams: 0,
    max_transcodes: 0,
    max_remote_stream_bitrate_kbps: 0,
    max_local_stream_bitrate_kbps: 0,
    max_profiles: 5,
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

function editorFor(etag = '"old"'): AdminUserEditor {
  return {
    user: { ...user, id: 7, last_active_at: undefined },
    etag,
    profileContext: captureAdminUserAuthority(),
  } as AdminUserEditor;
}

function mount(props: Partial<Parameters<typeof AdminUserDeleteDialog>[0]> = {}) {
  const done = vi.fn();
  const close = vi.fn();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AdminUserDeleteDialog
        initialEditor={editorFor()}
        onClose={close}
        onDeleted={done}
        {...props}
      />
    </QueryClientProvider>,
  );
  return { done, close };
}

function typeName(text: string) {
  fireEvent.change(screen.getByLabelText("Type Target to confirm"), { target: { value: text } });
}

const problem412 = () =>
  new Response(
    JSON.stringify({
      type: "https://silo.example/problems/precondition_failed",
      title: "Changed",
      status: 412,
      detail: "Reload",
    }),
    { status: 412, headers: { "Content-Type": "application/problem+json", ETag: '"ignored"' } },
  );

it("says what goes with the account and waits for its exact name", () => {
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  mount({ profileCount: 3 });
  expect(screen.getByRole("alertdialog", { name: "Delete Target?" })).toHaveTextContent(
    "This removes the account, its 3 profiles, watch history, and saved preferences. It can't be undone.",
  );
  const remove = screen.getByRole("button", { name: "Delete account" });
  expect(remove).toBeDisabled();
  typeName("target");
  expect(remove).toBeDisabled();
  typeName("Target");
  expect(remove).toBeEnabled();
  // Without a caller that handles it, disabling is not offered.
  expect(screen.queryByRole("button", { name: "Disable instead" })).toBeNull();
  expect(fetch).not.toHaveBeenCalled();
});

it("disables the account instead, with the same validator", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const disabled = vi.fn();
  const { done } = mount({ onDisabled: disabled });
  expect(screen.getByText(/Want to keep the history\?/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Disable instead" }));
  await waitFor(() => expect(disabled).toHaveBeenCalledTimes(1));
  expect(done).not.toHaveBeenCalled();
  const [url, init] = fetch.mock.calls[0]!;
  expect(String(url)).toContain("/api/v2/admin/users/7");
  expect(init?.method).toBe("PUT");
  expect(new Headers(init?.headers).get("If-Match")).toBe('"old"');
  expect(JSON.parse(String(init?.body))).toEqual({ enabled: false });
});

it("does not offer to disable an account that already is", () => {
  vi.stubGlobal("fetch", vi.fn());
  render(
    <QueryClientProvider client={new QueryClient()}>
      <AdminUserDeleteDialog
        initialEditor={{ ...editorFor(), user: { ...editorFor().user, enabled: false } }}
        onClose={vi.fn()}
        onDeleted={vi.fn()}
        onDisabled={vi.fn()}
      />
    </QueryClientProvider>,
  );
  expect(screen.queryByRole("button", { name: "Disable instead" })).toBeNull();
  expect(screen.getByText(/its profiles, watch history/)).toBeInTheDocument();
});

it("retains delete confirmation after412 and uses only explicitly reloaded validator", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(problem412())
    .mockResolvedValueOnce(
      new Response(JSON.stringify(user), {
        headers: { "Content-Type": "application/json", ETag: '"fresh"' },
      }),
    )
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const { done, close } = mount();
  typeName("Target");
  fireEvent.click(screen.getByRole("button", { name: "Delete account" }));
  await screen.findByText("The user changed. Reload before trying again.");
  expect(screen.getByRole("button", { name: "Delete account" })).toBeDisabled();
  expect(close).not.toHaveBeenCalled();
  expect(fetch).toHaveBeenCalledTimes(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload current user" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete account" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Delete account" }));
  await waitFor(() => expect(done).toHaveBeenCalledTimes(1));
  expect(new Headers(fetch.mock.calls[0]![1]?.headers).get("If-Match")).toBe('"old"');
  expect(new Headers(fetch.mock.calls[2]![1]?.headers).get("If-Match")).toBe('"fresh"');
});
