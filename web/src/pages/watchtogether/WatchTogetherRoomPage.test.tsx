import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { useState } from "react";
import type { WatchTogetherRoomSnapshot, WatchTogetherSuggestion } from "@/lib/watchTogether";
import type { WatchTogetherRoomConnectionResult } from "@/player/hooks/useWatchTogetherRoomConnection";
import { WatchPlaybackControllerContext } from "@/playback/watchPlaybackContext";
import WatchTogetherRoomPage from "./WatchTogetherRoomPage";

const state = vi.hoisted(() => ({
  connection: null as unknown as WatchTogetherRoomConnectionResult,
  startPlayback: vi.fn(),
}));
vi.mock("@/player/hooks/useWatchTogetherRoomConnection", () => ({
  useWatchTogetherRoomConnection: () => state.connection,
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { id: 1 }, profile: { id: "p1" } }),
}));
vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (id?: string) => ({
    data: id
      ? {
          content_id: id,
          type: "movie",
          title: `Title ${id}`,
          year: 2024,
          runtime: 100,
          genres: [],
        }
      : undefined,
  }),
}));
// The shelf's rows have their own tests; here it is a bare surface that can
// hand a selection up so the stage-as-inspector flow is what's under test.
vi.mock("./room/browse/BrowseShelf", () => ({
  BrowseShelf: ({
    verb,
    collapsible,
    open,
    onSelect,
  }: {
    verb: string;
    collapsible?: boolean;
    open: boolean;
    onSelect: (s: {
      card: { content_id: string; type: string; title: string; year?: number };
    }) => void;
  }) => {
    const [query, setQuery] = useState("");
    return (
      <div data-testid="shelf" data-verb={verb} data-collapsed={String(!!collapsible && !open)}>
        <input
          aria-label="Shelf search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <button
          type="button"
          onClick={() =>
            onSelect({
              card: { content_id: "arrival", type: "movie", title: "Arrival", year: 2016 },
            })
          }
        >
          pick Arrival
        </button>
      </div>
    );
  },
}));
vi.mock("./room/browse/CandidateStage", () => ({
  CandidateStage: ({
    selection,
    verb,
    replaces,
    onConfirm,
    onDismiss,
  }: {
    selection: { card: { content_id: string; title: string; year?: number } };
    verb: string;
    replaces?: string;
    onConfirm: (c: {
      content_id: string;
      content_type: "movie";
      title: string;
      subtitle: string;
      poster_url: string;
    }) => void;
    onDismiss: () => void;
  }) => (
    <div data-testid="candidate" data-verb={verb}>
      Candidate: {selection.card.title}
      {replaces ? <span>Replaces {replaces}</span> : null}
      <button
        type="button"
        onClick={() =>
          onConfirm({
            content_id: selection.card.content_id,
            content_type: "movie",
            title: selection.card.title,
            subtitle: String(selection.card.year ?? ""),
            poster_url: "",
          })
        }
      >
        confirm
      </button>
      <button type="button" onClick={onDismiss}>
        dismiss
      </button>
    </div>
  ),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const member = (
  id: number,
  name: string,
  over: Partial<NonNullable<WatchTogetherRoomSnapshot["members"]>[number]> = {},
) => ({
  user_id: id,
  profile_id: `p${id}`,
  display_name: name,
  is_host: id === 1,
  is_self: id === 1,
  connected: true,
  lobby_ready: false,
  ...over,
});

function room(over: Partial<WatchTogetherRoomSnapshot> = {}): WatchTogetherRoomSnapshot {
  return {
    room_id: "room",
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
    member_count: 2,
    host_connected: true,
    self_role: "host",
    self_can_control_transport: true,
    self_can_manage_room: true,
    self_ignore_wait: false,
    invite_path: "/rooms/join?token=t",
    members: [member(1, "Nathan"), member(2, "Maya")],
    ...over,
  };
}

function connection(
  over: Partial<WatchTogetherRoomConnectionResult> = {},
): WatchTogetherRoomConnectionResult {
  return {
    connectionState: "connected",
    room: room(),
    suggestions: [],
    closedReason: null,
    replacementReason: null,
    rejoinRoom: vi.fn(),
    transportCommand: null,
    serverTimeOffsetMs: 0,
    sendRoomMessage: vi.fn(() => ({ ok: true })),
    updatePolicy: vi.fn(async () => null),
    selectItem: vi.fn(async () => null),
    stageItem: vi.fn(async () => null),
    startPlayback: vi.fn(async () => room({ phase: "playing", playback_state: "waiting" })),
    stopPlayback: vi.fn(async () => room({ selected_content_id: "dune", selection_revision: 2 })),
    updateSelectionMode: vi.fn(async () => room({ selection_mode: "vote" })),
    setLobbyReady: vi.fn(() => ({ ok: true })),
    closeRoom: vi.fn(async () => {}),
    createSuggestion: vi.fn(async () => {}),
    deleteSuggestion: vi.fn(async () => {}),
    vote: vi.fn(async () => {}),
    unvote: vi.fn(async () => {}),
    promoteSuggestion: vi.fn(async () => null),
    fallbackSource: vi.fn(async () => null),
    ...over,
  };
}

function renderPage(
  conn: WatchTogetherRoomConnectionResult,
  path:
    | string
    | { pathname: string; search: string; state: unknown } = "/rooms/room?room_token=proof",
) {
  state.connection = conn;
  const controller = {
    state: { request: null, mode: "foreground", pictureInPictureActive: false },
    startPlayback: state.startPlayback,
  } as never;
  const queryClient = new QueryClient();
  const page = () => (
    <QueryClientProvider client={queryClient}>
      <WatchPlaybackControllerContext.Provider value={controller}>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/rooms/:roomId" element={<WatchTogetherRoomPage />} />
            <Route path="/rooms" element={<div>hub</div>} />
          </Routes>
        </MemoryRouter>
      </WatchPlaybackControllerContext.Provider>
    </QueryClientProvider>
  );
  const view = render(page());
  return {
    ...view,
    rerenderConnection(next: WatchTogetherRoomConnectionResult) {
      state.connection = next;
      view.rerender(page());
    },
  };
}

beforeEach(() => {
  localStorage.clear();
  state.startPlayback.mockClear();
});
afterEach(cleanup);

describe("WatchTogetherRoomPage", () => {
  it("puts a shelf pick on the stage as a candidate and stages it on confirm", async () => {
    const conn = connection();
    renderPage(conn);
    expect(screen.getByText("Nothing on yet")).toBeInTheDocument();
    expect(screen.getByTestId("shelf")).toHaveAttribute("data-verb", "pick");
    expect(screen.getByTestId("shelf")).toHaveAttribute("data-collapsed", "false");
    fireEvent.click(screen.getByRole("button", { name: "pick Arrival" }));
    const candidate = screen.getByTestId("candidate");
    expect(candidate).toHaveTextContent("Candidate: Arrival");
    expect(candidate).toHaveAttribute("data-verb", "stage");
    expect(screen.queryByText("Nothing on yet")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "confirm" }));
    await waitFor(() =>
      expect(conn.stageItem).toHaveBeenCalledWith({ content_id: "arrival", library_id: undefined }),
    );
  });

  it("dismissing a candidate brings the phase's stage back", () => {
    renderPage(connection());
    fireEvent.click(screen.getByRole("button", { name: "pick Arrival" }));
    fireEvent.click(screen.getByRole("button", { name: "dismiss" }));
    expect(screen.queryByTestId("candidate")).toBeNull();
    expect(screen.getByText("Nothing on yet")).toBeInTheDocument();
  });

  it("switches to voting from the empty lobby tile", () => {
    const conn = connection();
    renderPage(conn);
    fireEvent.click(screen.getByRole("button", { name: /Open the floor to votes/ }));
    expect(conn.updateSelectionMode).toHaveBeenCalledWith("vote");
  });

  it("stages: host starts, label shows the ready count, and start is not gated on it", () => {
    const conn = connection({
      room: room({
        selected_content_id: "dune",
        members: [member(1, "Nathan"), member(2, "Maya", { lobby_ready: true })],
      }),
    });
    renderPage(conn);
    expect(screen.getByRole("heading", { name: /Title dune/ })).toBeInTheDocument();
    const start = screen.getByRole("button", { name: /Start for everyone · 1\/1 ready/ });
    expect(screen.getByText("Guest ready check")).toBeInTheDocument();
    expect(screen.queryByLabelText("not ready")).toBeNull();
    fireEvent.click(start);
    expect(conn.startPlayback).toHaveBeenCalledTimes(1);
  });

  it("guest in a staged lobby toggles ready over the socket and can suggest", () => {
    const conn = connection({
      room: room({
        selected_content_id: "dune",
        self_role: "guest",
        self_can_manage_room: false,
        members: [member(1, "Nathan", { is_self: false }), member(2, "Maya", { is_self: true })],
      }),
    });
    renderPage(conn);
    expect(screen.queryByRole("button", { name: /Start for everyone/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "I'm ready" }));
    expect(conn.setLobbyReady).toHaveBeenCalledWith(true);
    // A guest's shelf suggests, and it stays open: browsing is what guests do.
    expect(screen.getByTestId("shelf")).toHaveAttribute("data-verb", "suggest");
    expect(screen.getByTestId("shelf")).toHaveAttribute("data-collapsed", "false");
    fireEvent.click(screen.getByRole("button", { name: "pick Arrival" }));
    expect(screen.getByTestId("candidate")).toHaveAttribute("data-verb", "suggest");
    fireEvent.click(screen.getByRole("button", { name: "confirm" }));
    expect(conn.createSuggestion).toHaveBeenCalledTimes(1);
  });

  it("folds the shelf under a staged lobby for the host and names what a new pick replaces", () => {
    const conn = connection({ room: room({ selected_content_id: "dune" }) });
    renderPage(conn);
    expect(screen.getByTestId("shelf")).toHaveAttribute("data-collapsed", "true");
    fireEvent.click(screen.getByRole("button", { name: "Change" }));
    expect(screen.getByTestId("shelf")).toHaveAttribute("data-collapsed", "false");
    fireEvent.click(screen.getByRole("button", { name: "pick Arrival" }));
    expect(screen.getByTestId("candidate")).toHaveTextContent("Replaces Title dune");
  });

  it("drops a candidate when the room changes phase underneath it", () => {
    const conn = connection();
    const view = renderPage(conn);
    fireEvent.click(screen.getByRole("button", { name: "pick Arrival" }));
    expect(screen.getByTestId("candidate")).toBeInTheDocument();
    state.connection = connection({ room: room({ selection_mode: "vote" }) });
    view.rerender(
      <QueryClientProvider client={new QueryClient()}>
        <WatchPlaybackControllerContext.Provider
          value={
            {
              state: { request: null, mode: "foreground", pictureInPictureActive: false },
              startPlayback: state.startPlayback,
            } as never
          }
        >
          <MemoryRouter initialEntries={["/rooms/room?room_token=proof"]}>
            <Routes>
              <Route path="/rooms/:roomId" element={<WatchTogetherRoomPage />} />
            </Routes>
          </MemoryRouter>
        </WatchPlaybackControllerContext.Provider>
      </QueryClientProvider>,
    );
    expect(screen.queryByTestId("candidate")).toBeNull();
  });

  it("vote mode ranks suggestions and lets the host play any of them", () => {
    const suggestions: WatchTogetherSuggestion[] = [
      {
        id: "b",
        room_id: "room",
        suggester_user_id: 2,
        suggester_profile_id: "p2",
        content_id: "dune",
        content_type: "movie",
        title: "Dune",
        subtitle: "",
        poster_url: "",
        note: "big screen",
        vote_count: 1,
        voted_by_me: false,
        created_at: "2026-01-01T00:00:01Z",
      },
      {
        id: "a",
        room_id: "room",
        suggester_user_id: 2,
        suggester_profile_id: "p2",
        content_id: "arrival",
        content_type: "movie",
        title: "Arrival",
        subtitle: "",
        poster_url: "",
        note: "",
        vote_count: 3,
        voted_by_me: true,
        created_at: "2026-01-01T00:00:00Z",
      },
    ];
    const conn = connection({ room: room({ selection_mode: "vote" }), suggestions });
    renderPage(conn);
    expect(screen.queryByText(/ready check/i)).toBeNull();
    expect(screen.getByRole("heading", { name: /Title arrival/ })).toBeInTheDocument();
    expect(screen.getByText("Leading")).toBeInTheDocument();
    expect(screen.getByText(/big screen/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Play Arrival for everyone/ }));
    expect(conn.promoteSuggestion).toHaveBeenCalledWith("a");
    const playButtons = screen.getAllByRole("button", { name: "Play this" });
    expect(playButtons).toHaveLength(2);
    fireEvent.click(playButtons[1]!);
    expect(conn.promoteSuggestion).toHaveBeenLastCalledWith("b");
    fireEvent.click(screen.getByRole("button", { name: "Vote for Dune" }));
    expect(conn.vote).toHaveBeenCalledWith("b");
  });

  it("host-pick lobby lists guest suggestions without votes and lets the host queue one", () => {
    const suggestions: WatchTogetherSuggestion[] = [
      {
        id: "a",
        room_id: "room",
        suggester_user_id: 2,
        suggester_profile_id: "p2",
        content_id: "arrival",
        content_type: "movie",
        title: "Arrival",
        subtitle: "",
        poster_url: "",
        note: "",
        vote_count: 0,
        voted_by_me: false,
        created_at: "2026-01-01T00:00:00Z",
      },
    ];
    const conn = connection({ room: room({ selection_mode: "host_pick" }), suggestions });
    renderPage(conn);
    expect(screen.getByRole("region", { name: "Suggestions" })).toBeInTheDocument();
    expect(screen.getByText("Arrival")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Vote for/ })).toBeNull();
    expect(screen.queryByText(/^0 votes$/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Queue this" }));
    expect(conn.stageItem).toHaveBeenCalledWith({ content_id: "arrival" });
  });

  it("auto-enters the player when playback starts, and offers rejoin on the room page", () => {
    const playing = room({
      phase: "playing",
      playback_state: "playing",
      selection_revision: 1,
      selected_content_id: "dune",
    });
    const conn = connection({ room: playing });
    renderPage(conn);
    expect(state.startPlayback).toHaveBeenCalledTimes(1);
    expect(state.startPlayback.mock.calls[0]![0]).toMatchObject({
      contentId: "dune",
      roomId: "room",
      roomToken: "proof",
      restart: true,
    });
    // The room started playing; this viewer pressed nothing, so it is not timed.
    expect(state.startPlayback.mock.calls[0]![1]).toBe("automatic");
    fireEvent.click(screen.getByRole("button", { name: "Rejoin playback" }));
    expect(state.startPlayback).toHaveBeenCalledTimes(2);
    expect(state.startPlayback.mock.calls[1]![1]).toBe("viewer");
    expect(screen.getByText(/After this/)).toBeInTheDocument();
  });

  it("lets the host stop playback for everyone from the playing stage", () => {
    const conn = connection({
      room: room({
        phase: "playing",
        playback_state: "playing",
        selection_revision: 1,
        selected_content_id: "dune",
      }),
    });
    renderPage(conn);
    fireEvent.click(screen.getByRole("button", { name: "Stop for everyone" }));
    expect(conn.stopPlayback).toHaveBeenCalledTimes(1);
  });

  it("hides Stop from guests on the playing stage", () => {
    const conn = connection({
      room: room({
        phase: "playing",
        playback_state: "playing",
        selection_revision: 1,
        selected_content_id: "dune",
        self_role: "guest",
        self_can_manage_room: false,
      }),
    });
    renderPage(conn);
    expect(screen.queryByRole("button", { name: "Stop for everyone" })).toBeNull();
    expect(screen.getByRole("button", { name: "Rejoin playback" })).toBeInTheDocument();
  });

  it("does not re-enter the player when the player just left for this selection", () => {
    // The player returns here when the video ends or the viewer exits. The
    // room is still playing, so without the location state a fresh mount
    // would launch the player again and the two pages would ping-pong.
    const playing = room({
      phase: "playing",
      playback_state: "playing",
      selection_revision: 1,
      selected_content_id: "dune",
      selected_file_id: 7,
    });
    renderPage(connection({ room: playing }), {
      pathname: "/rooms/room",
      search: "?room_token=proof",
      state: { suppressAutoStartSelection: { contentId: "dune", fileId: 7 } },
    });
    expect(state.startPlayback).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Rejoin playback" })).toBeInTheDocument();
  });

  it("does not auto-start on a staged lobby (revision unchanged)", () => {
    renderPage(connection({ room: room({ selected_content_id: "dune" }) }));
    expect(state.startPlayback).not.toHaveBeenCalled();
  });

  it("stays in the room after exit when another selection already started, then follows later starts", () => {
    const playing = room({
      phase: "playing",
      playback_state: "playing",
      selection_revision: 3,
      selected_content_id: "arrival",
      selected_file_id: 8,
    });
    const view = renderPage(connection({ room: playing }), {
      pathname: "/rooms/room",
      search: "?room_token=proof",
      state: { suppressAutoStartSelection: { contentId: "dune", fileId: 7 } },
    });
    expect(state.startPlayback).not.toHaveBeenCalled();
    view.rerenderConnection(connection({ room: { ...playing, generation: 2 } }));
    expect(state.startPlayback).not.toHaveBeenCalled();
    view.rerenderConnection(
      connection({ room: { ...playing, phase: "lobby", selection_revision: 4 } }),
    );
    view.rerenderConnection(connection({ room: { ...playing, selection_revision: 5 } }));
    expect(state.startPlayback).toHaveBeenCalledTimes(1);
    expect(state.startPlayback).toHaveBeenCalledWith(
      expect.objectContaining({ contentId: "arrival", fileId: 8 }),
      "automatic",
    );
  });

  it("keeps a guest's shelf search when the room starts playing", () => {
    const staged = room({
      selected_content_id: "dune",
      self_can_manage_room: false,
      self_role: "guest",
    });
    const view = renderPage(connection({ room: staged }));
    fireEvent.change(screen.getByRole("textbox", { name: "Shelf search" }), {
      target: { value: "arrival" },
    });
    view.rerenderConnection(
      connection({ room: { ...staged, phase: "playing", selection_revision: 1 } }),
    );
    expect(screen.getByRole("textbox", { name: "Shelf search" })).toHaveValue("arrival");
    expect(screen.getByTestId("shelf")).toHaveAttribute("data-collapsed", "true");
  });

  it("hides the mode switch for guests and while playing", () => {
    renderPage(connection({ room: room({ self_can_manage_room: false, self_role: "guest" }) }));
    expect(screen.queryByRole("button", { name: "Change how the room picks" })).toBeNull();
    cleanup();
    renderPage(
      connection({
        room: room({
          phase: "playing",
          playback_state: "playing",
          selected_content_id: "x",
          selection_revision: 1,
        }),
      }),
    );
    expect(screen.queryByRole("button", { name: "Change how the room picks" })).toBeNull();
    cleanup();
    renderPage(connection());
    expect(screen.getByRole("button", { name: "Change how the room picks" })).toBeInTheDocument();
  });

  it("renders the terminal state when the room closes and remembers it ended", () => {
    localStorage.setItem(
      "silo.watchParty.recentRooms.v1",
      JSON.stringify([
        {
          room_id: "room",
          code: "KX7Q2M",
          token: "proof",
          user_id: 1,
          profile_id: "p1",
          role: "host",
          last_seen_at: new Date().toISOString(),
        },
      ]),
    );
    renderPage(connection({ room: null, closedReason: "host_left" }));
    expect(screen.getByRole("alert")).toHaveTextContent("The room has ended.");
    expect(JSON.parse(localStorage.getItem("silo.watchParty.recentRooms.v1")!)[0].ended).toBe(true);
  });

  it("offers explicit rejoin after replacement without ending the room or starting playback", () => {
    const conn = connection({
      connectionState: "disconnected",
      replacementReason: "This profile joined the Watch Party on another device.",
      room: room({ phase: "playing", selected_content_id: "arrival", selection_revision: 1 }),
    });
    renderPage(conn);
    expect(screen.getByRole("alert")).toHaveTextContent(conn.replacementReason!);
    expect(state.startPlayback).not.toHaveBeenCalled();
    expect(JSON.parse(localStorage.getItem("silo.watchParty.recentRooms.v1")!)[0].ended).not.toBe(
      true,
    );
    fireEvent.click(screen.getByRole("button", { name: "Rejoin Watch Party" }));
    expect(conn.rejoinRoom).toHaveBeenCalledOnce();
  });
});
