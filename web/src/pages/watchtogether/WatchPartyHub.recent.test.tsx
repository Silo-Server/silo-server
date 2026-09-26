import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import WatchPartyHub from "./WatchPartyHub";
import { RECENT_ROOMS_KEY } from "./hooks/useRecentRooms";

vi.mock("react-router", () => ({ useSearchParams: () => [new URLSearchParams()] }));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => vi.fn() }));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { id: 2 }, profile: { id: "guest" } }),
}));

const room = (phase: string) => ({
  room: {
    room_id: "room-1",
    phase,
    playback_state: phase === "playing" ? "playing" : "idle",
    selection_mode: "host_pick",
    selection_revision: 1,
    code: "KX7Q2M",
    guest_control_policy: "host_only",
    is_paused: false,
    anchor_position_seconds: 0,
    anchor_updated_at: "2026-01-01T00:00:00Z",
    generation: 3,
    member_count: 2,
    host_connected: true,
    self_role: "guest",
    self_can_control_transport: false,
    self_can_manage_room: false,
    self_ignore_wait: false,
    invite_path: "/rooms/join?token=t",
  },
  room_access_token: "renewed",
});
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
    },
  });

function remember(over: object = {}) {
  localStorage.setItem(
    RECENT_ROOMS_KEY,
    JSON.stringify([
      {
        room_id: "room-1",
        code: "KX7Q2M",
        title: "Dune: Part Two",
        token: "proof",
        user_id: 2,
        profile_id: "guest",
        role: "guest",
        last_seen_at: new Date().toISOString(),
        ...over,
      },
    ]),
  );
}

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("guest");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("WatchPartyHub recent parties", () => {
  it.each(["network", "server"])(
    "keeps Rejoin available when the status check has a %s failure",
    async (failure) => {
      remember();
      vi.stubGlobal(
        "fetch",
        failure === "network"
          ? vi.fn().mockRejectedValue(new TypeError("Network unavailable"))
          : vi.fn().mockResolvedValue(json({ type: "internal_error", status: 500 }, 500)),
      );
      render(<WatchPartyHub />);
      expect(await screen.findByText("Status unavailable")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Rejoin" })).toBeInTheDocument();
      expect(screen.queryByText("Ended")).toBeNull();
      expect(JSON.parse(localStorage.getItem(RECENT_ROOMS_KEY)!)[0].ended).not.toBe(true);
    },
  );

  it("verifies a remembered room and offers Rejoin when it is still live", async () => {
    remember();
    let resolve!: (r: Response) => void;
    const fetch = vi.fn().mockImplementation(() => new Promise<Response>((r) => (resolve = r)));
    vi.stubGlobal("fetch", fetch);
    render(<WatchPartyHub />);
    expect(screen.getByText("Checking…")).toBeInTheDocument();
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    expect(String(fetch.mock.calls[0]![0])).toMatch(/\/watch-together\/rooms\/room-1$/);
    // A late answer must still land: the hook republishes the list on mount,
    // which used to cancel the in-flight check and strand the row.
    resolve(json(room("playing")));
    expect(await screen.findByRole("button", { name: "Rejoin" })).toBeInTheDocument();
    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it("marks the room ended when the read says so", async () => {
    remember();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(json({ type: "conflict", status: 409 }, 409)));
    render(<WatchPartyHub />);
    await waitFor(() => expect(screen.getAllByText("Ended").length).toBeGreaterThan(0));
    expect(screen.queryByRole("button", { name: "Rejoin" })).toBeNull();
    expect(JSON.parse(localStorage.getItem(RECENT_ROOMS_KEY)!)[0].ended).toBe(true);
  });
});
