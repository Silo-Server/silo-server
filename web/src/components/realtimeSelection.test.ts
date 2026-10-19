import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { EventChannel } from "@/api/types";
import { createRealtimeBackoff } from "@/lib/realtimeRecovery";
import { createRealtimeSelection } from "./realtimeSelection";
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});
afterEach(() => vi.useRealTimers());
function fixture() {
  let channels: EventChannel[] = ["catalog"];
  let id = 0;
  const send = vi.fn((_message: Record<string, unknown>) => true);
  const selection = createRealtimeSelection({
    channels: () => channels,
    send,
    active: () => true,
    requestId: () => `req-${++id}`,
    backoff: createRealtimeBackoff(() => 0),
  });
  return {
    selection,
    send,
    desire: (next: EventChannel[]) => {
      channels = next;
      selection.changed();
    },
  };
}
it("batches the complete latest selection once per microtask and retains old accepted state on refusal", async () => {
  const f = fixture();
  f.desire(["catalog"]);
  f.desire(["catalog", "notifications"]);
  await vi.advanceTimersByTimeAsync(0);
  expect(f.send).toHaveBeenCalledTimes(1);
  expect(f.send.mock.calls[0]![0]).toMatchObject({
    channels: ["catalog", "notifications"],
    request_id: "req-1",
  });
  expect(f.selection.receive({ type: "subscribed", request_id: "req-1" }, true)).toBe(true);
  f.desire(["catalog"]);
  await vi.advanceTimersByTimeAsync(0);
  f.selection.receive(
    {
      type: "error",
      code: "realtime_rate_limited",
      request_id: "req-2",
      work_started: false,
      retry_after_ms: 2_000,
    },
    true,
  );
  expect(f.selection.accepted()).toEqual(["catalog", "notifications"]);
  await vi.advanceTimersByTimeAsync(1_999);
  expect(f.send).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(1);
  expect(f.send).toHaveBeenCalledTimes(3);
  expect(f.send.mock.calls[2]![0]).toMatchObject({ channels: ["catalog"], request_id: "req-3" });
  f.selection.dispose();
});
it("cancels a replaced desire without bypassing its class floor or accepting stale acknowledgements", async () => {
  const f = fixture();
  f.desire(["catalog"]);
  await vi.advanceTimersByTimeAsync(0);
  f.selection.receive(
    {
      type: "error",
      code: "realtime_unavailable",
      request_id: "req-1",
      work_started: false,
      retry_after_ms: 2_000,
    },
    true,
  );
  f.desire(["notifications"]);
  await vi.advanceTimersByTimeAsync(0);
  expect(f.selection.receive({ type: "subscribed", request_id: "req-1" }, true)).toBe(false);
  await vi.advanceTimersByTimeAsync(2_000);
  expect(f.send).toHaveBeenCalledTimes(2);
  expect(f.send.mock.calls[1]![0]).toMatchObject({ channels: ["notifications"] });
  f.selection.dispose();
  await vi.advanceTimersByTimeAsync(60_000);
  expect(f.send).toHaveBeenCalledTimes(2);
});
