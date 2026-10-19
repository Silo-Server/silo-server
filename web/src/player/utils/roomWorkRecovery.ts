import { createRealtimeBackoff, readWorkRefusal } from "@/lib/realtimeRecovery";
import type { WatchTogetherRoomSnapshot } from "@/lib/watchTogether";

type Message = Record<string, unknown>;
type Kind =
  | "attach_session"
  | "ready"
  | "buffering"
  | "lobby_ready"
  | "transport_request"
  | "state_report";
export interface RoomSendOptions {
  /** Re-read media/desired state at retry time; null cancels this desire. */
  current: () => Message | null;
}
export interface RoomWorkContext {
  room: WatchTogetherRoomSnapshot | null;
  commandId: string | null;
  epoch: number;
  active: boolean;
  supported: boolean;
}
export interface RoomRecoveryNotice {
  id: number;
  message: string;
  uncertain: boolean;
  discarded?: boolean;
}
interface Pending {
  id: string;
  kind: Kind;
  message: Message;
  fence: string;
  desire: string;
  current?: () => Message | null;
  deadline: number;
  retried: boolean;
  refused: boolean;
  timer: ReturnType<typeof setTimeout> | null;
}
const recoveryKinds = new Set(["attach_session", "ready", "buffering", "state_report"]);
const desiredKinds = new Set(["attach_session", "ready", "buffering", "lobby_ready"]);
const workKinds = new Set([...recoveryKinds, ...desiredKinds, "transport_request"]);
const workClass = (kind: string) => (recoveryKinds.has(kind) ? "recovery" : "action");
function fence(context: RoomWorkContext, message: Message) {
  const room = context.room;
  return JSON.stringify([
    context.epoch,
    room?.room_id,
    room?.selection_revision,
    room?.generation,
    room?.phase,
    room?.attached_session_id,
    context.commandId,
    room?.self_can_control_transport,
    room?.self_can_manage_room,
    message.session_id,
  ]);
}
function desire(message: Message) {
  return JSON.stringify([message.type, message.session_id, message.command_id, message.ready]);
}
function echoed(context: RoomWorkContext, pending: Pending) {
  const room = context.room;
  const self = room?.members?.find((member) => member.is_self);
  switch (pending.kind) {
    case "attach_session":
      return room?.attached_session_id === pending.message.session_id;
    case "ready":
      return self?.is_ready === true;
    case "buffering":
      return self?.is_buffering === true;
    case "lobby_ready":
      return room?.phase === "lobby" && self?.lobby_ready === pending.message.ready;
    default:
      return false;
  }
}

/** Per engagement. This is correlation and current desire, never a replay ledger. */
export function createRoomWorkRecovery(options: {
  context: () => RoomWorkContext;
  send: (message: Message) => boolean;
  backoff: ReturnType<typeof createRealtimeBackoff>;
  notice: (notice: RoomRecoveryNotice) => void;
  reconcile: () => void;
}) {
  let nextId = 0;
  let nextNotice = 0;
  let disposed = false;
  const pending = new Map<string, Pending>();
  const floor = { recovery: 0, action: 0 };
  let latestTransportId: string | null = null;
  const drop = (entry: Pending) => {
    if (entry.timer !== null) clearTimeout(entry.timer);
    entry.timer = null;
    pending.delete(entry.id);
  };
  const discard = (entry: Pending) => {
    drop(entry);
    options.notice({
      id: ++nextNotice,
      uncertain: false,
      discarded: true,
      message: "The room action was refused and was not applied. Try again when the room is ready.",
    });
  };
  const uncertain = (
    entry: Pending,
    message = "The room action was not confirmed. Check the room before trying again.",
  ) => {
    if (entry.refused) {
      discard(entry);
      return;
    }
    drop(entry);
    options.notice({ id: ++nextNotice, message, uncertain: true });
    options.reconcile();
  };
  const valid = (entry: Pending) => {
    const context = options.context();
    if (disposed || !context.active || fence(context, entry.message) !== entry.fence) return false;
    if (entry.kind === "lobby_ready" && context.room?.phase !== "lobby") return false;
    if (
      entry.kind === "transport_request" &&
      (entry.id !== latestTransportId ||
        context.room?.phase !== "playing" ||
        (entry.message.action === "seek"
          ? !context.room.self_can_manage_room
          : !context.room.self_can_control_transport) ||
        !context.room.attached_session_id ||
        (entry.message.session_id !== undefined &&
          entry.message.session_id !== context.room.attached_session_id))
    )
      return false;
    return true;
  };
  const cancel = (kind: string) => {
    for (const entry of pending.values()) if (entry.kind === kind) drop(entry);
  };
  const schedule = (entry: Pending, delay: number) => {
    if (entry.timer !== null) clearTimeout(entry.timer);
    entry.timer = setTimeout(() => run(entry), Math.max(0, delay));
  };
  const transmit = (entry: Pending) => {
    entry.refused = false;
    if (!options.send({ ...entry.message, request_id: entry.id })) {
      if (entry.kind === "transport_request") uncertain(entry);
      else drop(entry);
      return false;
    }
    // A send is not an acknowledgement. Desired states wait for an echo;
    // transport waits for its correlated applied receipt and never replays on timeout.
    schedule(
      entry,
      entry.kind === "transport_request" ? Math.max(0, entry.deadline - performance.now()) : 1_500,
    );
    return true;
  };
  function run(entry: Pending) {
    entry.timer = null;
    if (!pending.has(entry.id)) return;
    if (!valid(entry)) {
      if (entry.kind === "transport_request")
        uncertain(
          entry,
          "The room changed before that action could be confirmed. Try again if needed.",
        );
      else drop(entry);
      return;
    }
    if (echoed(options.context(), entry)) {
      drop(entry);
      return;
    }
    if (entry.kind === "transport_request") {
      if (!entry.refused || entry.retried || performance.now() >= entry.deadline) {
        uncertain(entry);
        return;
      }
      if (performance.now() < floor.action) {
        schedule(entry, Math.min(floor.action, entry.deadline) - performance.now());
        return;
      }
      entry.retried = true;
      transmit(entry);
      return;
    }
    const current = entry.current?.() ?? (entry.current ? null : entry.message);
    if (!current || desire(current) !== entry.desire) {
      drop(entry);
      return;
    }
    entry.message = current;
    const delay = floor[workClass(entry.kind)] - performance.now();
    if (delay > 0) {
      schedule(entry, delay);
      return;
    }
    transmit(entry);
  }
  return {
    send(message: Message, retry?: RoomSendOptions): { ok: boolean; requestId?: string } {
      const context = options.context();
      if (disposed || !context.active) return { ok: false };
      const kind = message.type as Kind;
      if (workKinds.has(kind) && !context.room) return { ok: false };
      const id = `room-${context.epoch}-${kind}-${++nextId}`;
      if (!context.supported || !workKinds.has(kind)) {
        return { ok: options.send({ ...message, request_id: id }) };
      }
      const isDesired = desiredKinds.has(kind);
      if (isDesired) {
        for (const entry of pending.values())
          if (entry.kind === kind) {
            if (entry.desire === desire(message) && valid(entry)) {
              entry.current = retry?.current ?? entry.current;
              return { ok: true, requestId: entry.id };
            }
            drop(entry);
          }
      }
      const entry: Pending = {
        id,
        kind,
        message,
        current: retry?.current,
        fence: fence(context, message),
        desire: desire(message),
        deadline: performance.now() + 5_000,
        retried: false,
        refused: false,
        timer: null,
      };
      if (isDesired && echoed(context, entry)) return { ok: true };
      if (kind === "transport_request") {
        latestTransportId = id;
        // A newer action cancels known-refused intent, even when the floor blocks it.
        // Unconfirmed sends retain their correlation until receipt or uncertainty.
        for (const previous of pending.values())
          if (previous.kind === kind && previous.refused) discard(previous);
      }
      if (!valid(entry)) return { ok: false };
      const delay = floor[workClass(kind)] - performance.now();
      if (kind === "state_report") {
        if (delay > 0) return { ok: false };
        const ok = options.send({ ...message, request_id: id });
        return { ok };
      }
      if (kind === "transport_request" && delay > 0) {
        options.notice({
          id: ++nextNotice,
          uncertain: false,
          discarded: true,
          message: "Room controls are waiting. Try again in a moment.",
        });
        return { ok: false };
      }
      pending.set(id, entry);
      if (delay > 0) schedule(entry, delay);
      else if (!transmit(entry)) return { ok: false };
      return { ok: true, requestId: id };
    },
    receive(message: Message) {
      const id = typeof message.request_id === "string" ? message.request_id : null;
      const entry = id ? pending.get(id) : undefined;
      const refusal = options.context().supported ? readWorkRefusal(message) : null;
      // A report has no pending intent, but its correlated refusal still sets
      // the recovery class floor in this connection epoch. No report body is retained or replayed.
      const reportPrefix = `room-${options.context().epoch}-state_report-`;
      const reportSequence = id?.startsWith(reportPrefix)
        ? Number(id.slice(reportPrefix.length))
        : NaN;
      const report =
        Number.isSafeInteger(reportSequence) && reportSequence > 0 && reportSequence <= nextId;
      if (refusal && (entry || report)) {
        const kind = entry?.kind ?? "state_report";
        const delay = options.backoff.refusal(refusal);
        floor[workClass(kind)] = Math.max(floor[workClass(kind)], performance.now() + delay);
        if (entry) {
          entry.refused = true;
          if (
            entry.kind === "transport_request" &&
            (entry.retried || performance.now() + delay >= entry.deadline || !valid(entry))
          ) {
            discard(entry);
          } else schedule(entry, delay);
        }
        return;
      }
      if (!entry) return;
      if (
        message.type === "action_result" &&
        message.status === "applied" &&
        message.action === entry.kind
      ) {
        options.backoff.useful();
        drop(entry);
        return;
      }
      if (message.type === "error") {
        // Missing/true work_started and unknown errors are never replay permission.
        entry.refused = false;
        if (entry.kind === "transport_request") uncertain(entry);
        else {
          drop(entry);
          options.reconcile();
        }
      }
    },
    sync() {
      for (const entry of pending.values()) {
        if (echoed(options.context(), entry)) {
          options.backoff.useful();
          drop(entry);
        } else if (!valid(entry) && entry.kind !== "transport_request") drop(entry);
      }
    },
    cancel,
    disconnect() {
      for (const entry of pending.values()) {
        if (entry.kind === "transport_request") uncertain(entry);
        else drop(entry);
      }
      options.backoff.interrupted();
    },
    dispose() {
      disposed = true;
      const settlement = { unknownDelivery: false, knownRefused: false };
      for (const entry of pending.values()) {
        if (entry.kind === "transport_request") {
          if (entry.refused) settlement.knownRefused = true;
          else settlement.unknownDelivery = true;
        }
        drop(entry);
      }
      return settlement;
    },
  };
}
