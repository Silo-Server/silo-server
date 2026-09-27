import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureRoomCreationDraft, createRoom } from "./watchTogetherCreate";
import WatchPartyHub from "@/pages/watchtogether/WatchPartyHub";
const state = vi.hoisted(() => ({ token: "", navigate: vi.fn() }));
vi.mock("react-router", () => ({
  useSearchParams: () => [new URLSearchParams({ token: state.token })],
}));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => state.navigate }));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/useAuth", () => ({ useOptionalAuth: () => null }));
function response(id: string) {
  return new Response(
    JSON.stringify({
      room: {
        room_id: id,
        phase: "lobby",
        playback_state: "idle",
        selection_mode: "host_pick",
        selection_revision: 0,
        code: "retained",
        guest_control_policy: "host_only",
        is_paused: true,
        anchor_position_seconds: 0,
        anchor_updated_at: "2026-01-01T00:00:00Z",
        generation: 1,
        member_count: 0,
        host_connected: false,
        self_role: "host",
        self_can_control_transport: true,
        self_can_manage_room: true,
        self_ignore_wait: false,
      },
      room_access_token: "proof",
    }),
    { status: 201, headers: { "Content-Type": "application/json" } },
  );
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("host");
  setProfileToken(null);
  state.token = "";
  state.navigate.mockClear();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("retains fallback UUID and mode across an uncertain explicit retry", async () => {
  vi.stubGlobal("crypto", {
    getRandomValues: (a: Uint8Array) => {
      a.forEach((_, i) => (a[i] = i));
      return a;
    },
  });
  const draft = captureRoomCreationDraft("vote");
  expect(draft.body.room_id).toBe("00010203-0405-4607-8809-0a0b0c0d0e0f");
  const fetch = vi
    .fn()
    .mockRejectedValueOnce(new Error("uncertain"))
    .mockResolvedValueOnce(response(draft.body.room_id));
  vi.stubGlobal("fetch", fetch);
  await expect(createRoom(draft)).rejects.toThrow("uncertain");
  await createRoom(draft);
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms");
  expect(fetch.mock.calls[0]![1].body).toBe(fetch.mock.calls[1]![1].body);
  expect(JSON.parse(fetch.mock.calls[0]![1].body)).toEqual(draft.body);
});
it.each([401, 403, 409, 422, 500])("single sends refusal %s", async (status) => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
  vi.stubGlobal("fetch", fetch);
  await expect(createRoom(captureRoomCreationDraft("host_pick"))).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("refuses old authority before dispatch and mismatched creation receipts", async () => {
  const draft = captureRoomCreationDraft("host_pick");
  setProfileToken("new");
  const fetch = vi.fn().mockResolvedValue(response("other"));
  vi.stubGlobal("fetch", fetch);
  await expect(createRoom(draft)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
  await expect(createRoom(captureRoomCreationDraft("host_pick"))).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it("mounted create reuses uncertain draft and changes identity only for new mode", async () => {
  const fetch = vi.fn().mockRejectedValue(new Error("uncertain"));
  vi.stubGlobal("fetch", fetch);
  const view = render(<WatchPartyHub />);
  fireEvent.click(view.getByRole("button", { name: "Create Watch Party" }));
  await view.findByText("uncertain");
  fireEvent.click(view.getByRole("button", { name: "Create Watch Party" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
  expect(fetch.mock.calls[0]![1].body).toBe(fetch.mock.calls[1]![1].body);
  fireEvent.click(view.getByRole("radio", { name: /Everyone votes/ }));
  await waitFor(() =>
    expect(view.getByRole("button", { name: "Create Watch Party" })).not.toBeDisabled(),
  );
  fireEvent.click(view.getByRole("button", { name: "Create Watch Party" }));
  await waitFor(() => expect(fetch).toHaveBeenCalledTimes(3));
  const a = JSON.parse(fetch.mock.calls[0]![1].body),
    b = JSON.parse(fetch.mock.calls[2]![1].body);
  expect(a.room_id).not.toBe(b.room_id);
  expect(b.selection_mode).toBe("vote");
});
it.each([
  ["mode", 201],
  ["authority", 201],
  ["unmount", 201],
  ["mode", 500],
  ["authority", 500],
  ["unmount", 500],
] as const)(
  "mounted create fences stale completion after %s replacement (%s)",
  async (kind, status) => {
    const pending: Array<(r: Response) => void> = [];
    const fetch = vi
      .fn()
      .mockImplementation(() => new Promise<Response>((resolve) => pending.push(resolve)));
    vi.stubGlobal("fetch", fetch);
    const view = render(<WatchPartyHub />);
    fireEvent.click(view.getByRole("button", { name: "Create Watch Party" }));
    await waitFor(() => expect(pending.length).toBe(1));
    if (kind === "mode") fireEvent.click(view.getByRole("radio", { name: /Everyone votes/ }));
    if (kind === "authority") {
      setProfileToken("new");
      view.rerender(<WatchPartyHub />);
    }
    if (kind === "unmount") view.unmount();
    if (kind !== "unmount") {
      fireEvent.click(view.getByRole("button", { name: "Create Watch Party" }));
      await waitFor(() => expect(pending.length).toBe(2));
    }
    await act(async () =>
      pending[0]!(
        status === 201
          ? response(JSON.parse(fetch.mock.calls[0]![1].body).room_id)
          : new Response(null, { status }),
      ),
    );
    expect(state.navigate).not.toHaveBeenCalled();
    if (kind !== "unmount") {
      expect(view.getByRole("button", { name: "Creating..." })).toBeDisabled();
      await act(async () =>
        pending[1]!(response(JSON.parse(fetch.mock.calls[1]![1].body).room_id)),
      );
      expect(state.navigate).toHaveBeenCalledTimes(1);
    }
  },
);
