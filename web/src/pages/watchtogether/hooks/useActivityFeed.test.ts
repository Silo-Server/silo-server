import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";
import type {
  WatchTogetherRoomMember,
  WatchTogetherRoomSnapshot,
  WatchTogetherSuggestion,
} from "@/lib/watchTogether";
import { diffSnapshots, diffSuggestions, foldActivity, useActivityFeed } from "./useActivityFeed";

const member = (
  id: number,
  name: string,
  extra: Partial<WatchTogetherRoomMember> = {},
): WatchTogetherRoomMember => ({
  user_id: id,
  profile_id: `p${id}`,
  display_name: name,
  is_host: id === 1,
  is_self: id === 1,
  connected: true,
  lobby_ready: false,
  ...extra,
});

const room = (over: Partial<WatchTogetherRoomSnapshot> = {}): WatchTogetherRoomSnapshot => ({
  room_id: "room",
  phase: "lobby",
  playback_state: "idle",
  selection_mode: "host_pick",
  selection_revision: 0,
  code: "ROOM",
  guest_control_policy: "host_only",
  is_paused: true,
  anchor_position_seconds: 0,
  anchor_updated_at: "2026-01-01T00:00:00Z",
  generation: 1,
  member_count: 1,
  host_connected: true,
  self_role: "host",
  self_can_control_transport: true,
  self_can_manage_room: true,
  self_ignore_wait: false,
  members: [member(1, "Nathan")],
  ...over,
});

const suggestion = (id: string, title: string): WatchTogetherSuggestion => ({
  id,
  room_id: "room",
  suggester_user_id: 2,
  suggester_profile_id: "p2",
  content_id: `c-${id}`,
  content_type: "movie",
  title,
  subtitle: "",
  poster_url: "",
  note: "",
  vote_count: 0,
  voted_by_me: false,
  created_at: "2026-01-01T00:00:00Z",
});

describe("diffSnapshots", () => {
  it("reports joins, leaves, ready toggles, staging, starting, mode and policy", () => {
    const before = room();
    const after = room({
      members: [member(1, "Nathan"), member(2, "Maya", { lobby_ready: true })],
      selected_content_id: "dune",
      selection_mode: "host_pick",
      guest_control_policy: "guest_play_pause",
      generation: 2,
    });
    const kinds = diffSnapshots(before, after, 1).map((e) => e.kind);
    expect(kinds).toEqual(["joined", "policy", "staged"]);
    const started = diffSnapshots(
      after,
      room({ ...after, phase: "playing", playback_state: "waiting" }),
      2,
    );
    expect(started.map((e) => e.kind)).toEqual(["started"]);
    expect(started[0]?.contentId).toBe("dune");
    const stopped = diffSnapshots(
      room({ ...after, phase: "playing", playback_state: "playing" }),
      room({ ...after, phase: "lobby", playback_state: "idle", selection_revision: 2 }),
      3,
    );
    expect(stopped.map((e) => [e.kind, e.contentId])).toEqual([["stopped", "dune"]]);
    const left = diffSnapshots(after, room({ ...after, members: [member(1, "Nathan")] }), 3);
    expect(left.map((e) => [e.kind, e.who])).toEqual([["left", "Maya"]]);
    const ready = diffSnapshots(
      room({ members: [member(1, "Nathan"), member(2, "Maya")] }),
      room({ members: [member(1, "Nathan"), member(2, "Maya", { lobby_ready: true })] }),
      4,
    );
    expect(ready.map((e) => [e.kind, e.who])).toEqual([["ready", "Maya"]]);
    const mode = diffSnapshots(
      after,
      room({ ...after, selection_mode: "vote", selected_content_id: undefined }),
      5,
    );
    expect(mode.map((e) => e.kind)).toEqual(["mode"]);
  });

  it("dedupes playback state while playing and ignores waiting", () => {
    const playing = room({ phase: "playing", playback_state: "playing", selected_content_id: "x" });
    expect(diffSnapshots(playing, { ...playing, playback_state: "waiting" }, 1)).toEqual([]);
    expect(
      diffSnapshots(playing, { ...playing, playback_state: "paused" }, 1).map((e) => e.kind),
    ).toEqual(["playback"]);
  });
});

describe("diffSuggestions", () => {
  it("names the suggester from the member list", () => {
    const current = room({ members: [member(1, "Nathan"), member(2, "Maya")] });
    const added = diffSuggestions(current, [], [suggestion("a", "Arrival")], 1);
    expect(added).toMatchObject([{ kind: "suggested", who: "Maya", detail: "Arrival" }]);
    const removed = diffSuggestions(current, [suggestion("a", "Arrival")], [], 2);
    expect(removed).toMatchObject([{ kind: "unsuggested", who: "Maya" }]);
  });
});

describe("useActivityFeed", () => {
  it("baselines on the first connected snapshot and diffs after", () => {
    const first = room();
    const { result, rerender } = renderHook(
      ({
        r,
        s,
        c,
      }: {
        r: WatchTogetherRoomSnapshot | null;
        s: WatchTogetherSuggestion[];
        c: "connected" | "connecting";
      }) => useActivityFeed(r, s, c, () => 42),
      {
        initialProps: {
          r: first as WatchTogetherRoomSnapshot | null,
          s: [] as WatchTogetherSuggestion[],
          c: "connecting" as "connected" | "connecting",
        },
      },
    );
    expect(result.current).toEqual([]);
    // Connected: the baseline, no entries even though members appeared.
    const joined = room({ members: [member(1, "Nathan"), member(2, "Maya")] });
    rerender({ r: joined, s: [], c: "connected" });
    expect(result.current).toEqual([]);
    // A later change is diffed.
    rerender({
      r: room({
        ...joined,
        members: [member(1, "Nathan"), member(2, "Maya", { lobby_ready: true })],
      }),
      s: [],
      c: "connected",
    });
    expect(result.current.map((e) => e.kind)).toEqual(["ready"]);
    rerender({ r: result.current && joined, s: [suggestion("a", "Arrival")], c: "connected" });
    expect(result.current.map((e) => e.kind)).toEqual(["suggested", "unready", "ready"]);
  });

  it("caps at fifty and resets for a different room", () => {
    let log = foldActivity(
      { roomId: null, connected: false, room: null, suggestions: null, entries: [] },
      room(),
      [],
      "connected",
      0,
    );
    let current = room();
    for (let i = 0; i < 60; i++) {
      const next = room({ ...current, members: [member(1, "Nathan"), member(2 + i, `G${i}`)] });
      log = foldActivity(log, next, [], "connected", i + 1);
      current = next;
    }
    expect(log.entries.length).toBe(50);
    const other = foldActivity(log, room({ room_id: "other" }), [], "connected", 99);
    expect(other.entries).toEqual([]);
    expect(other.roomId).toBe("other");
  });
});
