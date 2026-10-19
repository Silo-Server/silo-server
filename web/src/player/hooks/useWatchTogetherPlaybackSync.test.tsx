import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { WatchTogetherRoomSnapshot, WatchTogetherTransportCommand } from "@/lib/watchTogether";
import { createRealtimeBackoff } from "@/lib/realtimeRecovery";
import { createRoomWorkRecovery, type RoomSendOptions } from "../utils/roomWorkRecovery";
import type { WatchTogetherRoomConnectionResult } from "./useWatchTogetherRoomConnection";
import { useWatchTogetherPlaybackSync } from "./useWatchTogetherPlaybackSync";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});
function fixture(waiting = false, attached = true) {
  const video = document.createElement("video");
  Object.defineProperties(video, {
    readyState: { value: 4, writable: true },
    seeking: { value: false, writable: true },
    paused: { value: false, writable: true },
  });
  video.currentTime = 10;
  let room = {
    room_id: "room",
    phase: "playing",
    playback_state: waiting ? "waiting" : "playing",
    self_role: "host",
    selection_revision: 1,
    generation: 1,
    attached_session_id: attached ? "session" : undefined,
    self_can_control_transport: true,
    self_can_manage_room: true,
    members: [
      {
        user_id: 1,
        profile_id: "profile",
        display_name: "Synthetic viewer",
        is_host: true,
        connected: true,
        is_self: true,
        is_ready: false,
        is_buffering: false,
      },
    ],
  } as WatchTogetherRoomSnapshot;
  let command: WatchTogetherTransportCommand = {
    command_id: "command",
    session_id: "session",
    selection_revision: 1,
    action: "play",
    position_seconds: 10,
    execute_at: new Date(0).toISOString(),
    issued_at: new Date(0).toISOString(),
    playback_state: room.playback_state,
  };
  const send = vi.fn((_message: Record<string, unknown>) => true);
  const recovery = createRoomWorkRecovery({
    context: () => ({
      room,
      commandId: command.command_id,
      epoch: 1,
      active: true,
      supported: true,
    }),
    send,
    backoff: createRealtimeBackoff(() => 0),
    notice: vi.fn(),
    reconcile: vi.fn(),
  });
  const videoRef = { current: video };
  const streamOriginRef = { current: 0 };
  const appliedCommandIdRef = { current: "command" };
  const sendRoomMessage = (message: Record<string, unknown>, retry?: RoomSendOptions) =>
    recovery.send(message, retry);
  const view = renderHook(() =>
    useWatchTogetherPlaybackSync({
      sessionId: "session",
      videoRef,
      streamOriginRef,
      appliedCommandIdRef,
      roomConnection: {
        connectionState: "connected",
        room,
        transportCommand: command,
        serverTimeOffsetMs: 0,
        workAdmission: { version: 1, backend: "memory", scope: "node", epoch: "test" },
        sendRoomMessage,
        cancelRoomMessage: recovery.cancel,
      } as WatchTogetherRoomConnectionResult,
    }),
  );
  const messages = (kind: string) =>
    send.mock.calls.map(([message]) => message).filter((message) => message.type === kind);
  const refuse = (kind: string, delay = 500) => {
    const message = messages(kind).at(-1)!;
    recovery.receive({
      type: "error",
      code: "realtime_rate_limited",
      request_id: message.request_id,
      work_started: false,
      retry_after_ms: delay,
    });
  };
  const update = (
    next: Partial<WatchTogetherRoomSnapshot>,
    nextCommand?: Partial<WatchTogetherTransportCommand>,
  ) => {
    room = { ...room, ...next };
    if (nextCommand) command = { ...command, ...nextCommand };
    recovery.sync();
    view.rerender();
  };
  return {
    video,
    view,
    recovery,
    messages,
    refuse,
    update,
    close: () => {
      view.unmount();
      recovery.dispose();
    },
  };
}
it("retries attach until its current session is echoed and cancels stale selection retries", async () => {
  const f = fixture(false, false);
  expect(f.messages("attach_session")).toHaveLength(1);
  act(() => f.refuse("attach_session"));
  await act(() => vi.advanceTimersByTimeAsync(500));
  expect(f.messages("attach_session")).toHaveLength(2);
  act(() => f.update({ attached_session_id: "session" }));
  await act(() => vi.advanceTimersByTimeAsync(2_000));
  expect(f.messages("attach_session")).toHaveLength(2);
  f.close();
});
it("does not mark readiness saved by sending and honors the refusal floor for ready and reports", async () => {
  const f = fixture(true);
  act(() => {
    f.view.result.current.reportReady();
    f.view.result.current.reportReady();
  });
  expect(f.messages("ready")).toHaveLength(1);
  act(() => f.refuse("ready", 2_000));
  await act(() => vi.advanceTimersByTimeAsync(1_999));
  expect(f.messages("ready")).toHaveLength(1);
  expect(f.messages("state_report")).toHaveLength(0);
  await act(() => vi.advanceTimersByTimeAsync(1));
  expect(f.messages("ready")).toHaveLength(2);
  act(() =>
    f.update({
      members: [
        {
          user_id: 1,
          profile_id: "profile",
          display_name: "Synthetic viewer",
          is_host: true,
          connected: true,
          is_self: true,
          is_ready: true,
        },
      ],
    }),
  );
  const count = f.messages("ready").length;
  await act(() => vi.advanceTimersByTimeAsync(2_000));
  expect(f.messages("ready")).toHaveLength(count);
  f.close();
});
it("rechecks current media readiness before retrying a refused periodic catch-up acknowledgement", async () => {
  const f = fixture();
  act(() => f.update({ self_ignore_wait: true }));
  await act(() => vi.advanceTimersByTimeAsync(500));
  expect(f.messages("ready")).toHaveLength(1);
  act(() => f.refuse("ready"));
  Object.defineProperty(f.video, "readyState", { value: 1, writable: true });
  await act(() => vi.advanceTimersByTimeAsync(500));
  expect(f.messages("ready")).toHaveLength(1);
  f.close();
});
it.each([false, true])(
  "retries refused buffering only while still stalled (healed:%s)",
  async (healed) => {
    const f = fixture();
    Object.defineProperty(f.video, "readyState", { value: 1, writable: true });
    act(() => f.view.result.current.reportBuffering());
    await act(() => vi.advanceTimersByTimeAsync(2_000));
    expect(f.messages("buffering")).toHaveLength(1);
    act(() => f.refuse("buffering"));
    if (healed)
      act(() => {
        Object.defineProperty(f.video, "readyState", { value: 4, writable: true });
        f.video.dispatchEvent(new Event("canplay"));
      });
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(f.messages("buffering")).toHaveLength(healed ? 1 : 2);
    f.close();
  },
);
it("keeps a refused buffering desire when a readiness check still fails", async () => {
  const f = fixture();
  Object.defineProperty(f.video, "readyState", { value: 1, writable: true });
  act(() => f.view.result.current.reportBuffering());
  await act(() => vi.advanceTimersByTimeAsync(2_000));
  act(() => {
    f.refuse("buffering");
    expect(f.view.result.current.reportReady().ok).toBe(false);
  });
  await act(() => vi.advanceTimersByTimeAsync(500));
  expect(f.messages("buffering")).toHaveLength(2);
  f.close();
});
it("sends only the latest absolute seek in the 250ms window and preserves distinct pause/play order", async () => {
  const f = fixture();
  act(() => {
    expect(f.view.result.current.requestTransport("seek", 20, false).ok).toBe(true);
  });
  await act(() => vi.advanceTimersByTimeAsync(100));
  act(() => f.view.result.current.requestTransport("seek", 40, false));
  expect(f.messages("transport_request")).toHaveLength(0);
  await act(() => vi.advanceTimersByTimeAsync(150));
  expect(f.messages("transport_request").map((m) => m.position_seconds)).toEqual([40]);
  act(() => {
    f.view.result.current.requestTransport("pause", 40, true);
    f.view.result.current.requestTransport("play", 40, false);
  });
  expect(f.messages("transport_request").map((m) => m.action)).toEqual(["seek", "pause", "play"]);
  f.close();
});
it("cancels a seek input window when command/selection authority changes", async () => {
  const f = fixture();
  act(() => f.view.result.current.requestTransport("seek", 20, false));
  act(() =>
    f.update({ selection_revision: 2 }, { command_id: "new-command", selection_revision: 2 }),
  );
  await act(() => vi.advanceTimersByTimeAsync(250));
  expect(f.messages("transport_request")).toHaveLength(0);
  f.close();
});
