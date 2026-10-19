import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createRealtimeBackoff } from "@/lib/realtimeRecovery";
import type { WatchTogetherRoomSnapshot } from "@/lib/watchTogether";
import { createRoomWorkRecovery, type RoomWorkContext } from "./roomWorkRecovery";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});
afterEach(() => vi.useRealTimers());
function fixture() {
  const context: RoomWorkContext = {
    epoch: 1,
    active: true,
    supported: true,
    commandId: "command-1",
    room: {
      room_id: "room",
      phase: "playing",
      selection_revision: 1,
      generation: 1,
      attached_session_id: "session",
      self_can_control_transport: true,
      self_can_manage_room: true,
      members: [{ is_self: true, is_ready: false, is_buffering: false }],
    } as WatchTogetherRoomSnapshot,
  };
  const send = vi.fn((_message: Record<string, unknown>) => true);
  const notice = vi.fn();
  const reconcile = vi.fn();
  const recovery = createRoomWorkRecovery({
    context: () => context,
    send,
    notice,
    reconcile,
    backoff: createRealtimeBackoff(() => 0),
  });
  const last = () => send.mock.calls.at(-1)![0];
  const refuse = (changes = {}) =>
    recovery.receive({
      type: "error",
      code: "realtime_rate_limited",
      request_id: last().request_id,
      retry_after_ms: 500,
      work_started: false,
      ...changes,
    });
  return { context, send, notice, reconcile, recovery, last, refuse };
}
const transport = {
  type: "transport_request",
  session_id: "session",
  action: "pause",
  position_seconds: 10,
  is_paused: true,
};
it("retries transport once only after its explicit correlated refusal, with the same request ID", () => {
  const f = fixture();
  f.recovery.send(transport);
  const sent = f.last();
  f.refuse();
  vi.advanceTimersByTime(499);
  expect(f.send).toHaveBeenCalledTimes(1);
  vi.advanceTimersByTime(1);
  expect(f.send).toHaveBeenCalledTimes(2);
  expect(f.last()).toEqual(sent);
  f.refuse();
  vi.advanceTimersByTime(5_000);
  expect(f.send).toHaveBeenCalledTimes(2);
  expect(f.notice).toHaveBeenCalledWith(expect.objectContaining({ discarded: true }));
  f.recovery.dispose();
});
it("accepts an applied result after operation snapshots and commands, without replay", () => {
  const f = fixture();
  f.recovery.send(transport);
  const id = f.last().request_id;
  f.context.room = { ...f.context.room!, generation: 2 };
  f.context.commandId = "command-2";
  f.recovery.sync();
  f.recovery.receive({
    type: "action_result",
    request_id: id,
    action: "transport_request",
    status: "applied",
  });
  vi.advanceTimersByTime(10_000);
  expect(f.send).toHaveBeenCalledTimes(1);
  expect(f.notice).not.toHaveBeenCalled();
  f.recovery.dispose();
});
it.each(["timeout", "downstream", "uncorrelated", "lost-ack", "close"])(
  "never replays uncertain transport (%s)",
  (reason) => {
    const f = fixture();
    f.recovery.send(transport);
    if (reason === "downstream") f.refuse({ work_started: true });
    if (reason === "uncorrelated") f.refuse({ request_id: "other" });
    if (reason === "lost-ack")
      f.recovery.receive({
        type: "action_result",
        request_id: f.last().request_id,
        action: "lobby_ready",
        status: "applied",
      });
    if (reason === "close") {
      f.recovery.disconnect();
      f.context.epoch++;
    }
    vi.advanceTimersByTime(10_000);
    expect(f.send).toHaveBeenCalledTimes(1);
    expect(f.reconcile).toHaveBeenCalled();
    expect(f.notice).toHaveBeenCalledWith(expect.objectContaining({ uncertain: true }));
    f.recovery.dispose();
  },
);
it.each(["room", "selection", "generation", "epoch", "session", "authority", "command"])(
  "cancels refused transport when its %s fence changes",
  (change) => {
    const f = fixture();
    f.recovery.send(transport);
    f.refuse();
    if (change === "room") f.context.room!.room_id = "other";
    if (change === "selection") f.context.room!.selection_revision++;
    if (change === "generation") f.context.room!.generation++;
    if (change === "epoch") f.context.epoch++;
    if (change === "session") f.context.room!.attached_session_id = "other";
    if (change === "authority") f.context.active = false;
    if (change === "command") f.context.commandId = "other";
    vi.advanceTimersByTime(1_000);
    expect(f.send).toHaveBeenCalledTimes(1);
    expect(f.notice).toHaveBeenCalledWith(expect.objectContaining({ discarded: true }));
    expect(f.notice).not.toHaveBeenCalledWith(expect.objectContaining({ uncertain: true }));
    expect(f.reconcile).not.toHaveBeenCalled();
    f.recovery.dispose();
  },
);
it("discards a retry beyond five seconds and never reorders an earlier refused pause after a later play", () => {
  const f = fixture();
  f.recovery.send(transport);
  const first = f.last();
  f.recovery.send({ ...transport, action: "play" });
  const second = f.last();
  f.recovery.receive({
    type: "error",
    code: "realtime_rate_limited",
    request_id: first.request_id,
    work_started: false,
    retry_after_ms: 500,
  });
  f.recovery.receive({
    type: "error",
    code: "realtime_rate_limited",
    request_id: second.request_id,
    work_started: false,
    retry_after_ms: 6_000,
  });
  vi.advanceTimersByTime(10_000);
  expect(f.send).toHaveBeenCalledTimes(2);
  f.recovery.dispose();
});
it("cancels an older refused transport retry when the newest intent is held by the class floor", () => {
  const f = fixture();
  f.recovery.send(transport);
  f.refuse();
  expect(f.recovery.send({ ...transport, action: "play" }).ok).toBe(false);
  const notices = f.notice.mock.calls.length;
  vi.advanceTimersByTime(5_000);
  expect(f.send).toHaveBeenCalledTimes(1);
  expect(f.notice).toHaveBeenCalledWith(expect.objectContaining({ discarded: true }));
  expect(f.notice).toHaveBeenCalledTimes(notices);
  expect(f.notice).not.toHaveBeenCalledWith(expect.objectContaining({ uncertain: true }));
  expect(f.reconcile).not.toHaveBeenCalled();
  f.recovery.dispose();
});
it("discards a known refusal on disconnect without uncertainty or a reconciliation read", () => {
  const f = fixture();
  f.recovery.send(transport);
  f.refuse();
  f.recovery.disconnect();
  vi.advanceTimersByTime(5_000);
  expect(f.send).toHaveBeenCalledTimes(1);
  expect(f.notice).toHaveBeenCalledWith(expect.objectContaining({ discarded: true }));
  expect(f.notice).not.toHaveBeenCalledWith(expect.objectContaining({ uncertain: true }));
  expect(f.reconcile).not.toHaveBeenCalled();
  f.recovery.dispose();
});
it.each(["unconfirmed", "refused", "applied", "retried"])(
  "reports unresolved delivery to teardown only for an unconfirmed send (%s)",
  (state) => {
    const f = fixture();
    f.recovery.send(transport);
    if (state === "refused" || state === "retried") f.refuse();
    if (state === "retried") vi.advanceTimersByTime(500);
    if (state === "applied")
      f.recovery.receive({
        type: "action_result",
        action: "transport_request",
        status: "applied",
        request_id: f.last().request_id,
      });
    expect(f.recovery.dispose()).toBe(state === "unconfirmed" || state === "retried");
    vi.advanceTimersByTime(10_000);
    expect(f.send).toHaveBeenCalledTimes(state === "retried" ? 2 : 1);
    expect(f.notice).not.toHaveBeenCalled();
    expect(f.reconcile).not.toHaveBeenCalled();
  },
);
it("treats a downstream error after refusal as uncertain without retrying", () => {
  const f = fixture();
  f.recovery.send(transport);
  f.refuse();
  f.refuse({ work_started: true });
  vi.advanceTimersByTime(5_000);
  expect(f.send).toHaveBeenCalledTimes(1);
  expect(f.notice).toHaveBeenCalledWith(expect.objectContaining({ uncertain: true }));
  expect(f.reconcile).toHaveBeenCalledTimes(1);
  f.recovery.dispose();
});
it.each(["phase", "session", "permission"])("rejects initial transport with stale %s", (change) => {
  const f = fixture();
  if (change === "phase") f.context.room!.phase = "lobby";
  if (change === "session") f.context.room!.attached_session_id = "other";
  if (change === "permission") f.context.room!.self_can_control_transport = false;
  expect(f.recovery.send(transport).ok).toBe(false);
  expect(f.send).not.toHaveBeenCalled();
  f.recovery.dispose();
});
it("binds a transport without an optional session field to the current attachment", () => {
  const f = fixture();
  expect(f.recovery.send({ type: "transport_request", action: "pause" }).ok).toBe(true);
  f.refuse();
  f.context.room!.attached_session_id = "new-session";
  vi.advanceTimersByTime(500);
  expect(f.send).toHaveBeenCalledTimes(1);
  f.recovery.dispose();
});
it.each(["attach_session", "ready", "buffering", "lobby_ready"])(
  "retains one current %s desire until authoritative acknowledgement",
  (kind) => {
    const f = fixture();
    if (kind === "lobby_ready") f.context.room!.phase = "lobby";
    if (kind === "attach_session") f.context.room!.attached_session_id = undefined;
    let position = 10;
    const current = () => ({
      type: kind,
      session_id: "session",
      command_id: "command-1",
      ready: true,
      position_seconds: position,
    });
    f.recovery.send(current(), { current });
    f.recovery.send(current(), { current });
    expect(f.send).toHaveBeenCalledTimes(1);
    f.refuse();
    position = 20;
    vi.advanceTimersByTime(500);
    expect(f.send).toHaveBeenCalledTimes(2);
    expect(f.last().position_seconds).toBe(20);
    if (kind === "attach_session") f.context.room!.attached_session_id = "session";
    if (kind === "ready") f.context.room!.members![0]!.is_ready = true;
    if (kind === "buffering") f.context.room!.members![0]!.is_buffering = true;
    if (kind === "lobby_ready")
      f.recovery.receive({
        type: "action_result",
        request_id: f.last().request_id,
        action: kind,
        status: "applied",
      });
    f.recovery.sync();
    vi.advanceTimersByTime(5_000);
    expect(f.send).toHaveBeenCalledTimes(2);
    f.recovery.dispose();
  },
);
it("does not retry healed buffering or lobby readiness outside the lobby", () => {
  const f = fixture();
  let stalled = true;
  const current = () => (stalled ? { type: "buffering", session_id: "session" } : null);
  f.recovery.send(current()!, { current });
  f.refuse();
  stalled = false;
  vi.advanceTimersByTime(500);
  expect(f.send).toHaveBeenCalledTimes(1);
  f.context.room!.phase = "lobby";
  f.recovery.send({ type: "lobby_ready", ready: true });
  f.refuse();
  f.context.room!.phase = "playing";
  f.recovery.sync();
  vi.advanceTimersByTime(5_000);
  expect(f.send).toHaveBeenCalledTimes(2);
  f.recovery.dispose();
});
it("applies recovery class floors to periodic reports and recomputed desires without mixing identities", () => {
  const f = fixture();
  f.recovery.send({ type: "state_report", session_id: "session", command_id: "command-1" });
  f.refuse({ retry_after_ms: 2_000 });
  expect(f.recovery.send({ type: "state_report", session_id: "session" }).ok).toBe(false);
  f.recovery.send({ type: "ready", session_id: "session", command_id: "command-1" });
  expect(f.send).toHaveBeenCalledTimes(1);
  f.context.commandId = "command-2";
  f.recovery.sync();
  vi.advanceTimersByTime(2_000);
  expect(f.send).toHaveBeenCalledTimes(1);
  f.recovery.send({ type: "state_report", session_id: "session", command_id: "command-2" });
  expect(f.last().command_id).toBe("command-2");
  f.recovery.dispose();
});
it("keeps legacy absent-metadata sends free of acknowledgement timers", () => {
  const f = fixture();
  f.context.supported = false;
  f.recovery.send(transport);
  f.refuse();
  vi.advanceTimersByTime(10_000);
  expect(f.send).toHaveBeenCalledTimes(1);
  expect(f.notice).not.toHaveBeenCalled();
  f.recovery.dispose();
});
