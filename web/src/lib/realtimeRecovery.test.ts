import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { V2ProblemError } from "@/api/v2/request";
import {
  createRealtimeBackoff,
  isInputShieldClose,
  readWorkAdmission,
  readWorkRefusal,
  ticketWorkRefusal,
} from "./realtimeRecovery";
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(0);
});
afterEach(() => vi.useRealTimers());
it("requires safe optional capability metadata", () => {
  const work_admission = { version: 1, backend: "redis", scope: "cluster", epoch: "synthetic" };
  expect(readWorkAdmission({ work_admission })).toEqual(work_admission);
  for (const value of [
    null,
    {},
    { work_admission: true },
    { work_admission: { ...work_admission, version: 2 } },
    { work_admission: { ...work_admission, epoch: 1 } },
  ])
    expect(readWorkAdmission(value)).toBeNull();
});
it("only explicit correlated pre-work refusal is replay permission", () => {
  const error = {
    type: "error",
    code: "realtime_rate_limited",
    work_started: false,
    request_id: "test",
    retry_after_ms: 500,
  };
  expect(readWorkRefusal(error)).toMatchObject({ requestId: "test", retryAfterMs: 500 });
  for (const changes of [
    { work_started: true },
    { work_started: undefined },
    { retry_after_ms: NaN },
    { retry_after_ms: "500" },
    { code: "busy" },
  ])
    expect(readWorkRefusal({ ...error, ...changes })).toBeNull();
});
it("honors both structured HTTP retry metadata and Retry-After", () => {
  const problem = {
    type: "urn:silo:problem:rate_limited",
    title: "Wait",
    detail: "Wait",
    instance: "test",
    status: 429,
    retry_after_ms: 2_500,
  };
  const error = new V2ProblemError("mintRoomSocketTicket", problem, 4);
  expect(ticketWorkRefusal(error)?.retryAfterMs).toBe(4_000);
  expect(ticketWorkRefusal(new Error("network"))).toBeNull();
});
it("retains outage, shield and opaque steps through opens until useful accepted activity", () => {
  const backoff = createRealtimeBackoff(() => 0);
  const refusal = { code: "realtime_unavailable" as const, retryAfterMs: 2_000 };
  expect([
    backoff.refusal(refusal),
    backoff.refusal(refusal),
    backoff.refusal(refusal),
    backoff.refusal(refusal),
    backoff.refusal(refusal),
    backoff.refusal(refusal),
  ]).toEqual([2_000, 4_000, 8_000, 16_000, 30_000, 30_000]);
  expect([
    backoff.reconnect(true),
    backoff.reconnect(true),
    backoff.reconnect(true),
    backoff.reconnect(true),
  ]).toEqual([5_000, 10_000, 20_000, 30_000]);
  expect([backoff.reconnect(), backoff.reconnect(), backoff.reconnect()]).toEqual([
    1_000, 2_000, 4_000,
  ]);
  backoff.useful();
  vi.advanceTimersByTime(29_999);
  backoff.useful();
  expect(backoff.reconnect(true)).toBe(30_000);
  backoff.useful();
  vi.advanceTimersByTime(30_000);
  backoff.useful();
  expect(backoff.reconnect(true)).toBe(5_000);
});
it("adds only nonnegative bounded jitter and distinguishes shield from other 1008 closes", () => {
  expect(createRealtimeBackoff(() => 1).reconnect()).toBe(1_250);
  expect(isInputShieldClose({ code: 1008, reason: "realtime input shield" })).toBe(true);
  expect(isInputShieldClose({ code: 1008, reason: "missing subscribe" })).toBe(false);
});
it("resets quiet accepted connections at disconnect without a second activity frame", () => {
  const backoff = createRealtimeBackoff(() => 0);
  expect(backoff.reconnect()).toBe(1_000);
  expect(backoff.reconnect()).toBe(2_000);
  backoff.useful();
  vi.advanceTimersByTime(30_000);
  backoff.interrupted();
  expect(backoff.reconnect()).toBe(1_000);
});
it("does not count open time or time following refusal as healthy accepted operation", () => {
  const backoff = createRealtimeBackoff(() => 0);
  expect(backoff.reconnect(true)).toBe(5_000);
  vi.advanceTimersByTime(300_000);
  backoff.interrupted();
  expect(backoff.reconnect(true)).toBe(10_000);
  backoff.useful();
  vi.advanceTimersByTime(29_999);
  backoff.refusal({ code: "realtime_unavailable", retryAfterMs: 2_000 });
  vi.advanceTimersByTime(300_000);
  backoff.interrupted();
  expect(backoff.reconnect(true)).toBe(20_000);
});
