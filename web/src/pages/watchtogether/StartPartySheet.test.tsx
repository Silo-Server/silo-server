import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { ItemDetail } from "@/api/types";
import { StartPartySheet } from "./StartPartySheet";

const nav = vi.hoisted(() => ({ navigate: vi.fn() }));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => nav.navigate }));
vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { id: 1 }, profile: { id: "p1" } }),
}));
vi.mock("@/hooks/queries/episodes", () => ({
  useSeasons: () => ({ data: { seasons: [] } }),
  useSeasonEpisodes: () => ({ data: { episodes: [] }, isLoading: false }),
}));
vi.mock("@/lib/watchTogetherActions", () => ({ copyWatchTogetherInvite: vi.fn(async () => true) }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const roomBody = (id: string, over: object = {}) => ({
  room: {
    room_id: id,
    phase: "lobby",
    playback_state: "idle",
    selection_mode: "host_pick",
    selection_revision: 0,
    code: "KX7Q2M",
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
    invite_path: "/rooms/join?token=t",
    ...over,
  },
  room_access_token: "proof",
});
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
/** The create receipt must carry the draft's own room id or the client refuses it. */
const draftRoomId = (init: RequestInit) => JSON.parse(String(init.body)).room_id as string;

const movie = {
  content_id: "dune",
  type: "movie",
  title: "Dune: Part Two",
  year: 2024,
  versions: [{}],
  genres: [],
} as unknown as ItemDetail;

beforeEach(() => {
  localStorage.clear();
  setAccessToken("login");
  setProfileId("p1");
  setProfileToken(null);
  nav.navigate.mockClear();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function renderSheet() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <StartPartySheet
        open
        onOpenChange={() => {}}
        item={movie}
        initialTarget={{ content_id: "dune", title: "Dune: Part Two", subtitle: "2024" }}
      />
    </QueryClientProvider>,
  );
}

describe("StartPartySheet", () => {
  it("creates, stages, remembers, copies and navigates in order", async () => {
    const fetch = vi.fn().mockImplementation(async (url: string, init: RequestInit) => {
      if (url.endsWith("/watch-together/rooms")) return json(roomBody(draftRoomId(init)), 201);
      if (url.endsWith("/staged-selection"))
        return json(
          roomBody(url.split("/").at(-2)!, {
            selected_content_id: "dune",
            generation: 2,
          }),
        );
      throw new Error(`unexpected ${url}`);
    });
    vi.stubGlobal("fetch", fetch);
    renderSheet();
    fireEvent.click(screen.getByRole("button", { name: "Create party & copy invite" }));
    await waitFor(() => expect(nav.navigate).toHaveBeenCalledTimes(1));
    const paths = fetch.mock.calls.map((c) => String(c[0]).replace(/^.*\/api\/v2/, ""));
    expect(paths[0]).toBe("/watch-together/rooms");
    expect(paths[1]).toMatch(/^\/watch-together\/rooms\/[0-9a-f-]{36}\/staged-selection$/);
    expect(JSON.parse(fetch.mock.calls[1]![1].body)).toEqual({ content_id: "dune" });
    expect(nav.navigate.mock.calls[0]![0]).toMatch(/^\/rooms\/[0-9a-f-]{36}\?room_token=proof$/);
    const recent = JSON.parse(localStorage.getItem("silo.watchParty.recentRooms.v1")!);
    expect(recent[0]).toMatchObject({
      code: "KX7Q2M",
      token: "proof",
      title: "Dune: Part Two · 2024",
    });
  });

  it("applies the pause policy when toggled and still navigates if staging fails", async () => {
    const fetch = vi.fn().mockImplementation(async (url: string, init: RequestInit) => {
      if (url.endsWith("/watch-together/rooms")) return json(roomBody(draftRoomId(init)), 201);
      if (url.endsWith("/policy"))
        return json(
          roomBody(url.split("/").at(-2)!, {
            guest_control_policy: "guest_play_pause",
            generation: 2,
          }),
        );
      if (url.endsWith("/staged-selection")) return new Response(null, { status: 422 });
      throw new Error(`unexpected ${url}`);
    });
    vi.stubGlobal("fetch", fetch);
    renderSheet();
    fireEvent.click(screen.getByRole("switch"));
    fireEvent.click(screen.getByRole("button", { name: "Create party & copy invite" }));
    await waitFor(() => expect(nav.navigate).toHaveBeenCalledTimes(1));
    const paths = fetch.mock.calls.map((c) =>
      String(c[0]).replace(/^.*\/api\/v2\/watch-together\/rooms\/[^/]+/, ""),
    );
    expect(paths[1]).toBe("/policy");
    expect(paths[2]).toBe("/staged-selection");
  });

  it("vote mode skips staging and hands the item to the room as the first suggestion", async () => {
    const fetch = vi.fn().mockImplementation(async (url: string, init: RequestInit) => {
      if (url.endsWith("/watch-together/rooms"))
        return json(roomBody(draftRoomId(init), { selection_mode: "vote" }), 201);
      throw new Error(`unexpected ${url}`);
    });
    vi.stubGlobal("fetch", fetch);
    renderSheet();
    fireEvent.click(screen.getByRole("radio", { name: /Everyone votes/ }));
    fireEvent.click(screen.getByRole("button", { name: "Create party & copy invite" }));
    await waitFor(() => expect(nav.navigate).toHaveBeenCalledTimes(1));
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(JSON.parse(fetch.mock.calls[0]![1].body).selection_mode).toBe("vote");
    expect(nav.navigate.mock.calls[0]![1]).toMatchObject({
      state: {
        suggestFirst: { content_id: "dune", content_type: "movie", title: "Dune: Part Two" },
      },
    });
  });

  it("aborts silently when the profile is replaced mid-flight", async () => {
    let resolve!: (r: Response) => void;
    const fetch = vi.fn().mockImplementation(() => new Promise<Response>((r) => (resolve = r)));
    vi.stubGlobal("fetch", fetch);
    renderSheet();
    fireEvent.click(screen.getByRole("button", { name: "Create party & copy invite" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    setProfileToken("replacement");
    resolve(json(roomBody(draftRoomId(fetch.mock.calls[0]![1])), 201));
    await new Promise((r) => setTimeout(r, 10));
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(nav.navigate).not.toHaveBeenCalled();
  });
});
