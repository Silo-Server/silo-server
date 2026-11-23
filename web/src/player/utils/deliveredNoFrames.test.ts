import { describe, expect, it } from "vitest";

import {
  DELIVERED_NO_FRAMES_MS,
  DeliveredNoFramesDeadline,
  MAX_DELIVERED_NO_FRAMES_RECOVERIES,
  type DeliveredNoFramesExpiry,
} from "./deliveredNoFrames";

function fixture(windowMs = DELIVERED_NO_FRAMES_MS) {
  let at = 0;
  const expiries: DeliveredNoFramesExpiry[] = [];
  const deadline = new DeliveredNoFramesDeadline(
    {
      now: () => at,
      onExpire: (expiry) => expiries.push(expiry),
    },
    windowMs,
  );
  return {
    deadline,
    expiries,
    setNow: (value: number) => {
      at = value;
    },
    advance: (by: number) => {
      at += by;
    },
  };
}

describe("DeliveredNoFramesDeadline", () => {
  it("recovers a delivered segment that never frames, inside its window", () => {
    const { deadline, expiries, advance } = fixture();

    deadline.delivered();
    advance(DELIVERED_NO_FRAMES_MS - 1);
    expect(deadline.poll()).toBe(false);
    expect(expiries).toHaveLength(0);

    advance(1);
    expect(deadline.poll()).toBe(true);
    expect(expiries).toEqual([{ waitedMs: DELIVERED_NO_FRAMES_MS, recoveries: 1 }]);
    // One expiry disarms the window, so a tick loop cannot recover repeatedly
    // for the same delivery.
    expect(deadline.armed).toBe(false);
    expect(deadline.poll()).toBe(false);
  });

  it("closes the window when a frame is presented", () => {
    const { deadline, expiries, advance } = fixture();

    deadline.delivered();
    advance(1_000);
    deadline.framed();
    advance(DELIVERED_NO_FRAMES_MS * 4);

    expect(deadline.poll()).toBe(false);
    expect(expiries).toHaveLength(0);
  });

  it("keeps one window open across the fragments a stall keeps delivering", () => {
    const { deadline, expiries, advance } = fixture();

    deadline.delivered();
    advance(1_000);
    deadline.delivered();
    advance(1_000);
    deadline.delivered();
    advance(DELIVERED_NO_FRAMES_MS - 2_000);

    expect(deadline.poll()).toBe(true);
    expect(expiries[0]?.waitedMs).toBe(DELIVERED_NO_FRAMES_MS);
  });

  it("does not fire while the viewer has paused", () => {
    const { deadline, expiries, advance } = fixture();

    deadline.delivered();
    deadline.setPlaying(false);
    advance(DELIVERED_NO_FRAMES_MS * 2);
    expect(deadline.poll()).toBe(false);

    // A pause ends the window; silence after resuming needs a new delivery.
    deadline.setPlaying(true);
    advance(DELIVERED_NO_FRAMES_MS * 2);
    expect(deadline.poll()).toBe(false);
    expect(expiries).toHaveLength(0);
  });

  // The HTML media element load algorithm clears `paused` and fires `emptied`
  // without firing `pause` when hls.js re-creates the MediaSource. That pause
  // is not the viewer's: the intent to play is unchanged, and a delivered
  // fragment still owes a frame, so the window must survive it.
  it("keeps the window armed through a pause the viewer did not ask for", () => {
    const { deadline, expiries, advance } = fixture();

    deadline.delivered();
    advance(1_000);
    deadline.setPlaying(false, { deliberate: false });
    advance(DELIVERED_NO_FRAMES_MS - 1_000);

    expect(deadline.poll()).toBe(true);
    expect(expiries).toEqual([{ waitedMs: DELIVERED_NO_FRAMES_MS, recoveries: 1 }]);
  });

  // A deliberate pause still ends the window, even when it arrives after the
  // silent one: the viewer asked for silence afterwards.
  it("clears a window kept through a silent pause when a viewer pause follows", () => {
    const { deadline, expiries, advance } = fixture();

    deadline.delivered();
    deadline.setPlaying(false, { deliberate: false });
    deadline.setPlaying(false);
    advance(DELIVERED_NO_FRAMES_MS * 2);

    expect(deadline.poll()).toBe(false);
    expect(expiries).toHaveLength(0);
  });

  it("bounds recoveries per transport and stops asking", () => {
    const { deadline, expiries, advance } = fixture();

    for (let attempt = 1; attempt <= MAX_DELIVERED_NO_FRAMES_RECOVERIES; attempt++) {
      deadline.delivered();
      advance(DELIVERED_NO_FRAMES_MS);
      expect(deadline.poll()).toBe(true);
    }
    expect(deadline.spent).toBe(true);
    expect(deadline.recoveryCount).toBe(MAX_DELIVERED_NO_FRAMES_RECOVERIES);

    deadline.delivered();
    advance(DELIVERED_NO_FRAMES_MS * 2);
    expect(deadline.poll()).toBe(false);
    expect(expiries).toHaveLength(MAX_DELIVERED_NO_FRAMES_RECOVERIES);
  });

  it("gives a replacement transport a fresh budget", () => {
    const { deadline, advance } = fixture();

    deadline.delivered();
    advance(DELIVERED_NO_FRAMES_MS);
    expect(deadline.poll()).toBe(true);
    expect(deadline.spent).toBe(false);

    deadline.reset();
    expect(deadline.recoveryCount).toBe(0);
    expect(deadline.armed).toBe(false);
  });
});
