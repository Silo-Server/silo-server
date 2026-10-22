import { describe, expect, it } from "vitest";
import vectors from "../../../docs/design/schemas/buffering-quality/v1/vectors.json";
import {
  BUFFERING_QUALITY_CONSTANTS,
  BUFFERING_QUALITY_COPY,
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

// The vectors pin the pill; the kept estimate is the adapter's.
it("keeps the estimate of a lowering adopted while the viewer's pick is pending", () => {
  const outputs: { type: string }[] = [];
  const kept: (number | undefined)[] = [];
  const c = new BufferingQualityController(
    "always",
    (output) => outputs.push(output),
    0,
    undefined,
    (kbps) => kept.push(kbps),
  );
  c.event({ type: "plan_adopted", plan }, 0);
  c.event({ type: "playing" }, 0);
  c.event({ type: "waiting" }, 20000);
  c.tick(26000);
  expect(outputs.at(-1)).toMatchObject({ type: "replan" });
  // The viewer picks while the lowering is in flight; the lowering lands first.
  c.event({ type: "pick_sent" }, 27000);
  c.event({ type: "plan_adopted", plan: { ...plan, plan_id: "p2", delivered_kbps: 30000 } }, 28000);
  expect(kept).toEqual([44100]);
  // The pick's plan clears the kept estimate as a quality change does.
  c.event({ type: "quality_changed" }, 29000);
  expect(kept).toEqual([44100, undefined]);
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

// C9: the adapter reports a start that showed no frame by its own deadline.
it.each<[string, BufferingMode, string, (c: BufferingQualityController) => void, boolean]>([
  ["lowers on Auto in the always mode", "always", "auto", () => {}, true],
  ["fails in the ask mode", "ask", "auto", () => {}, false],
  ["fails in the never mode", "never", "auto", () => {}, false],
  ["fails for a picked quality", "always", "4k", () => {}, false],
  [
    "fails after a Keep answer for the title",
    "ask",
    "auto",
    (c) => {
      c.event({ type: "playing" }, 0);
      c.event({ type: "waiting" }, 20000);
      c.tick(26000);
      c.event({ type: "select_keep" }, 27000);
      // The viewer switches to always afterwards.
      c.mode = "always";
    },
    false,
  ],
])("a start that misses its deadline %s", (_name, mode, quality, answer, lowers) => {
  const outputs: unknown[] = [];
  const c = new BufferingQualityController(mode, (output) => outputs.push(output), 0);
  c.event({ type: "plan_adopted", plan: { ...plan, quality } }, 0);
  answer(c);
  // A later plan of the same quality shows no frame by its deadline.
  c.event({ type: "plan_adopted", plan: { ...plan, plan_id: "p2", quality } }, 100000);
  outputs.length = 0;
  expect(c.startDeadlinePassed(160000)).toBe(lowers);
  expect(outputs).toEqual(
    lowers
      ? [
          {
            type: "replan",
            body: {
              operation: "quality_change",
              quality_preference: "auto",
              bandwidth_estimate_kbps: 44100,
              bandwidth_estimate_source: "observed_rebuffer",
            },
          },
        ]
      : [],
  );
});

it("omits an unknown delivered bitrate from the summary", () => {
  const summaries: BufferingSummary[] = [];
  const c = new BufferingQualityController(
    "always",
    () => {},
    0,
    (_plan, summary) => summaries.push(summary),
  );
  c.event({ type: "plan_adopted", plan: { ...plan, delivered_kbps: 0 } }, 0);
  c.stop(1000);
  expect(summaries[0]).not.toHaveProperty("delivered_kbps");
});

it("tracks and reports stalls in a Watch Party room without acting, then starts over on leaving", () => {
  const outputs: unknown[] = [];
  const summaries: BufferingSummary[] = [];
  const c = new BufferingQualityController(
    "always",
    (output) => outputs.push(output),
    0,
    (_plan, summary) => summaries.push(summary),
  );
  c.event({ type: "plan_adopted", plan }, 0);
  c.event({ type: "playing" }, 0);
  c.setSuspended(true, 15000);
  c.event({ type: "waiting" }, 20000);
  for (let t = 20100; t <= 30000; t += 100) c.tick(t);
  c.event({ type: "playing" }, 30000);
  expect(outputs).toEqual([]);
  // Leaving is not a plan boundary, and the room's long stall is forgotten.
  c.setSuspended(false, 40000);
  c.event({ type: "waiting" }, 50000);
  c.event({ type: "playing" }, 51500);
  expect(outputs).toEqual([]);
  c.event({ type: "waiting" }, 60000);
  for (let t = 60100; t <= 66000; t += 100) c.tick(t);
  expect(outputs).toHaveLength(1);
  c.stop(70000);
  expect(summaries).toHaveLength(1);
  expect(summaries[0]).toMatchObject({ stall_count: 3, buffering_action: "lowered" });
});

it("counts a stall that a pause ends in the summary's time but not its count", () => {
  const summaries: BufferingSummary[] = [];
  const c = new BufferingQualityController(
    "never",
    () => {},
    0,
    (_plan, summary) => summaries.push(summary),
  );
  c.event({ type: "plan_adopted", plan }, 0);
  c.event({ type: "playing" }, 0);
  c.event({ type: "waiting" }, 20000);
  c.event({ type: "pause" }, 23000);
  c.stop(30000);
  expect(summaries[0]).toMatchObject({
    stall_count: 0,
    stall_total_ms: 3000,
    stall_longest_ms: 3000,
  });
});

it("uses the vectors' constants and copy", () => {
  expect(BUFFERING_QUALITY_CONSTANTS).toEqual(vectors.constants);
  expect(BUFFERING_QUALITY_COPY).toEqual(vectors.copy);
});
