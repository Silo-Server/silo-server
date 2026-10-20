import { describe, expect, it } from "vitest";
import vectors from "../../../docs/design/schemas/buffering-quality/v1/vectors.json";
import {
  BufferingQualityController,
  type BufferingEvent,
  type BufferingMode,
  type BufferingSummary,
} from "./buffering-quality";

describe("buffering-quality wire vectors", () => {
  for (const vector of vectors.cases)
    it(vector.name, () => {
      const outputs: unknown[] = [];
      const summaries = new Map<string, BufferingSummary>();
      const c = new BufferingQualityController(
        ("mode" in vector ? vector.mode : vectors.defaults.mode) as BufferingMode,
        (output, t) => outputs.push({ t, ...output }),
        0,
        (plan, summary) => summaries.set(plan.adoption_id ?? plan.plan_id, summary),
      );
      // The clock moves one millisecond at a time, so every timer fires at
      // exactly the time it is due.
      let clock = 0;
      const advance = (to: number) => {
        while (clock < to) c.tick(++clock);
      };
      for (const event of vector.events) {
        advance(event.t);
        c.event(event as BufferingEvent, event.t);
      }
      advance(vector.until);
      expect(outputs).toEqual(vector.expect);
      if ("buffering_action" in vector) {
        // Each plan reports when it stops playing; the last one at the end.
        c.stop(vector.until);
        for (const [plan, action] of Object.entries(vector.buffering_action ?? {}))
          expect(summaries.get(plan)?.buffering_action, plan).toBe(action);
      }
    });
});

const plan = {
  plan_id: "p1",
  quality: "auto",
  delivered_kbps: 63000,
  available_qualities: [{ label: "1080p", bitrate_kbps: 10000 }],
};

it("does not request a new plan while stopping at the stall deadline", () => {
  const outputs: unknown[] = [];
  const c = new BufferingQualityController("always", (output) => outputs.push(output), 0);
  c.event({ type: "plan_adopted", plan }, 0);
  c.event({ type: "playing" }, 0);
  c.event({ type: "waiting" }, 20000);
  c.stop(26000);
  expect(outputs).toEqual([]);
});

it("reports one stall that outlasts a prompt's timeout as one stall", () => {
  const summaries: BufferingSummary[] = [];
  const c = new BufferingQualityController(
    "ask",
    () => {},
    0,
    (_plan, summary) => summaries.push(summary),
  );
  c.event({ type: "plan_adopted", plan }, 0);
  c.event({ type: "playing" }, 0);
  c.event({ type: "waiting" }, 20000);
  // The prompt shows at 26000 and times out at 41000, mid-stall.
  for (let t = 20100; t <= 50000; t += 100) c.tick(t);
  c.event({ type: "playing" }, 50000);
  c.stop(60000);
  expect(summaries).toEqual([
    expect.objectContaining({
      stall_count: 1,
      stall_total_ms: 30000,
      stall_longest_ms: 30000,
      buffering_action: "timed_out",
    }),
  ]);
});

it("does not keep the estimate of a lowering the player refused to send", () => {
  const kept: (number | undefined)[] = [];
  const outputs: unknown[] = [];
  const c = new BufferingQualityController(
    "always",
    (output) => outputs.push(output),
    0,
    undefined,
    (kbps) => kept.push(kbps),
  );
  c.event(
    { type: "plan_adopted", plan: { ...plan, available_qualities: [...plan.available_qualities] } },
    0,
  );
  c.event({ type: "playing" }, 0);
  c.event({ type: "waiting" }, 20000);
  for (let t = 20100; t <= 26000; t += 100) c.tick(t);
  expect(kept).toEqual([44100]);
  c.event({ type: "replan_failed", unsent: true }, 26000);
  expect(kept).toEqual([44100, undefined]);
  c.event({ type: "client_replan", operation: "track_change" }, 27000);
  expect(outputs.at(-1)).toEqual({
    type: "replan",
    body: { operation: "track_change", quality_preference: "auto" },
  });
});
