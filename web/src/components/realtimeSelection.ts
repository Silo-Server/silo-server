import type { EventChannel } from "@/api/types";
import { createRealtimeBackoff, readWorkRefusal } from "@/lib/realtimeRecovery";

/** One complete latest desire, one retry, and no replacement of accepted state on refusal. */
export function createRealtimeSelection(options: {
  channels: () => EventChannel[];
  send: (message: Record<string, unknown>) => boolean;
  requestId: () => string;
  active: () => boolean;
  backoff: ReturnType<typeof createRealtimeBackoff>;
}) {
  let disposed = false;
  let queued = false;
  let current: { requestId: string; channels: EventChannel[] } | null = null;
  let accepted: EventChannel[] = [];
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let notBefore = 0;
  const cancelTimer = () => {
    if (retryTimer !== null) clearTimeout(retryTimer);
    retryTimer = null;
  };
  const send = () => {
    cancelTimer();
    if (disposed || !options.active()) return;
    if (performance.now() < notBefore) {
      retryTimer = setTimeout(send, notBefore - performance.now());
      return;
    }
    const requestId = options.requestId();
    const channels = options.channels();
    if (options.send({ type: "subscribe", request_id: requestId, channels })) {
      current = { requestId, channels };
    }
  };
  return {
    changed() {
      current = null;
      cancelTimer();
      if (queued || disposed) return;
      queued = true;
      queueMicrotask(() => {
        queued = false;
        send();
      });
    },
    receive(message: Record<string, unknown>, supported: boolean) {
      if (
        !current ||
        (supported && message.request_id !== current.requestId) ||
        (message.request_id !== undefined && message.request_id !== current.requestId)
      )
        return false;
      if (message.type === "subscribed") {
        const channels = message.channels;
        accepted = Array.isArray(channels)
          ? current.channels.filter((channel) => channels.includes(channel))
          : current.channels;
        current = null;
        notBefore = 0;
        cancelTimer();
        options.backoff.useful();
        return true;
      }
      const refusal = supported ? readWorkRefusal(message) : null;
      if (!refusal || refusal.requestId !== current.requestId) return false;
      notBefore = Math.max(notBefore, performance.now() + options.backoff.refusal(refusal));
      cancelTimer();
      retryTimer = setTimeout(send, notBefore - performance.now());
      return false;
    },
    accepted: () => [...accepted],
    retryDelay: () => Math.max(0, notBefore - performance.now()),
    disconnected() {
      current = null;
      accepted = [];
      cancelTimer();
    },
    dispose() {
      disposed = true;
      current = null;
      cancelTimer();
    },
  };
}
