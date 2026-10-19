import { describe, expect, it, vi } from "vitest";
import { V2ProblemError } from "@/api/v2/request";
import { createEventSocketRecovery, eventSocketRetryAfterMs } from "./eventSocketRecovery";

describe("generic event socket recovery", () => {
  it.each([
    [0, [5_000, 10_000, 20_000, 30_000, 30_000]],
    [1, [7_500, 15_000, 30_000, 45_000, 45_000]],
  ])("uses the capped failure ladder at jitter endpoint %s", (sample, expected) => {
    const random = vi.fn(() => sample);
    const recovery = createEventSocketRecovery(random);
    expect(expected.map(() => recovery.reconnect())).toEqual(expected);
    expect(random).toHaveBeenCalledTimes(5);
  });

  it("draws jitter independently and raises the floor before adding it", () => {
    const random = vi.fn().mockReturnValueOnce(0).mockReturnValueOnce(1).mockReturnValueOnce(0.5);
    const recovery = createEventSocketRecovery(random);
    expect(recovery.reconnect(40_000)).toBe(40_000);
    expect(recovery.reconnect(40_000)).toBe(60_000);
    expect(recovery.reconnect(1_000)).toBe(25_000);
    expect(random).toHaveBeenCalledTimes(3);
  });

  it.each([30_000, 300_000])("resets after %s ms accepted, then fails at 5 seconds", (age) => {
    let now = 0;
    const recovery = createEventSocketRecovery(
      () => 1,
      () => now,
    );
    recovery.reconnect();
    recovery.reconnect();
    recovery.attempt();
    recovery.accepted();
    now = age;
    // Duplicate ACKs never move the start of the accepted interval.
    recovery.accepted();
    expect(recovery.reconnect()).toBe(1_500);
    recovery.attempt();
    expect(recovery.reconnect()).toBe(7_500);
  });

  it("does not reset for a short subscription or an open socket without an ACK", () => {
    let now = 0;
    const recovery = createEventSocketRecovery(
      () => 0,
      () => now,
    );
    recovery.attempt();
    recovery.accepted();
    now = 29_999;
    expect(recovery.reconnect()).toBe(5_000);
    recovery.attempt();
    now += 30_000;
    expect(recovery.reconnect()).toBe(10_000);
  });

  it("clears the accepted clock on an immediate replacement attempt", () => {
    let now = 0;
    const recovery = createEventSocketRecovery(
      () => 0,
      () => now,
    );
    recovery.accepted();
    now = 30_000;
    // access_changed and profile rebind can bypass the generic retry method.
    recovery.attempt();
    expect(recovery.reconnect()).toBe(5_000);
  });

  it("honors a larger readable floor even after healthy acceptance", () => {
    let now = 0;
    const recovery = createEventSocketRecovery(
      () => 1,
      () => now,
    );
    recovery.accepted();
    now = 30_000;
    expect(recovery.reconnect(8_000)).toBe(12_000);
    expect(recovery.reconnect()).toBe(7_500);
  });
});

describe("ticket mint retry floor", () => {
  it.each([null, -1, 0, Number.NaN, Number.POSITIVE_INFINITY, 8])(
    "reads valid Retry-After seconds: %s",
    (seconds) => {
      const error = new V2ProblemError(
        "mintEventsSocketTicket",
        {
          type: "about:blank",
          title: "Unavailable",
          status: 503,
          detail: "Fixture unavailable",
          instance: "/api/v2/events/ws-ticket",
        },
        seconds,
      );
      expect(eventSocketRetryAfterMs(error)).toBe(seconds === 8 ? 8_000 : 0);
    },
  );

  it("has no readable floor for an opaque error", () => {
    expect(eventSocketRetryAfterMs(new TypeError("fixture failure"))).toBe(0);
  });
});
