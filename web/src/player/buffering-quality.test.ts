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
        undefined,
        // The vectors leave out the player's measurement. A stall before the
        // attempt first plays lowers only on evidence that the plan's media
        // arrived more slowly than it plays, so every vector has it.
        () => 100,
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
// After `before`, a later plan is adopted at 100 s; after `during`, its start,
// it misses its deadline at 160 s, its media measured at `observed` kbps (null
// for no measurement). A row that lowers names its estimate.
it.each<{
  name: string;
  mode?: BufferingMode;
  quality?: string;
  before?: (c: BufferingQualityController) => void;
  later?: Partial<typeof plan>;
  during?: (c: BufferingQualityController) => void;
  observed?: number | null;
  estimate?: number;
}>([
  { name: "lowers on Auto in the always mode", estimate: 44100 },
  { name: "fails when nothing measured its media", observed: null },
  { name: "fails when its media arrived at its bitrate", observed: 63000 },
  { name: "fails in the ask mode", mode: "ask" },
  { name: "fails in the never mode", mode: "never" },
  { name: "fails for a picked quality", quality: "4k" },
  {
    name: "fails after a Keep answer for the title",
    mode: "ask",
    before: (c) => {
      c.event({ type: "playing" }, 0);
      c.event({ type: "waiting" }, 20000);
      c.tick(26000);
      c.event({ type: "select_keep" }, 27000);
      // The viewer switches to always afterwards.
      c.mode = "always";
    },
  },
  {
    name: "fails while the viewer's pick is pending",
    during: (c) => c.event({ type: "pick_sent" }, 150000),
  },
  {
    name: "lowers again when the lowered plan does not start either",
    before: (c) => expect(c.startDeadlinePassed(60000)).toBe(true),
    later: {
      delivered_kbps: 10000,
      available_qualities: [
        { label: "1080p", bitrate_kbps: 10000 },
        { label: "720p", bitrate_kbps: 4000 },
      ],
    },
    estimate: 7000,
  },
  {
    name: "fails during an outage",
    during: (c) => c.event({ type: "transport_error_start" }, 150000),
  },
  {
    name: "fails when an outage ended 3 s before",
    during: (c) => {
      c.event({ type: "transport_error_start" }, 150000);
      c.event({ type: "transport_error_end" }, 157000);
    },
  },
  {
    name: "lowers when an outage ended 6 s before",
    during: (c) => {
      c.event({ type: "transport_error_start" }, 140000);
      c.event({ type: "transport_error_end" }, 154000);
    },
    estimate: 44100,
  },
])(
  "a start that misses its deadline: $name",
  ({ mode = "always", quality = "auto", before, later, during, observed = 3000, estimate }) => {
    const outputs: unknown[] = [];
    const c = new BufferingQualityController(
      mode,
      (output) => outputs.push(output),
      0,
      undefined,
      undefined,
      () => observed ?? undefined,
    );
    c.event({ type: "plan_adopted", plan: { ...plan, quality } }, 0);
    before?.(c);
    c.event({ type: "plan_adopted", plan: { ...plan, plan_id: "p2", quality, ...later } }, 100000);
    during?.(c);
    outputs.length = 0;
    expect(c.startDeadlinePassed(160000)).toBe(estimate !== undefined);
    expect(outputs).toEqual(
      estimate === undefined
        ? []
        : [
            {
              type: "replan",
              body: {
                operation: "quality_change",
                quality_preference: "auto",
                bandwidth_estimate_kbps: estimate,
                bandwidth_estimate_source: "observed_rebuffer",
              },
            },
          ],
    );
  },
);

// C12: until a plan of the attempt plays, a wait may be a stream that cannot
// start for another reason. A stall then lowers only when the plan's media was
// measured (`measured` kbps) arriving below its 63000 kbps; after the attempt
// has played the usual rules apply. Each row adopts the plan at 0, then plays
// its events; a row that lowers names when.
it.each<{
  name: string;
  measured?: number;
  events: [number, BufferingEvent["type"]][];
  lowersAt?: number;
}>([
  {
    name: "a start's long wait lowers when its media arrived slowly",
    measured: 3000,
    events: [[0, "waiting"]],
    lowersAt: 16000,
  },
  {
    name: "a start's long wait does not lower when nothing measured its media",
    events: [[0, "waiting"]],
  },
  {
    name: "a start's long wait does not lower when its media arrived at its bitrate",
    measured: 63000,
    events: [[0, "waiting"]],
  },
  {
    name: "a start's wait without evidence does not count after its first frame",
    events: [
      [0, "waiting"],
      [14000, "playing"],
      [20000, "waiting"],
      [21500, "playing"],
    ],
  },
  {
    name: "a long stall after the first frame lowers without a measurement",
    events: [
      [0, "playing"],
      [20000, "waiting"],
    ],
    lowersAt: 26000,
  },
  {
    name: "a new attempt needs evidence again until it plays",
    events: [
      [0, "playing"],
      [5000, "new_attempt"],
      [5000, "waiting"],
    ],
  },
])("before the attempt first plays: $name", ({ measured, events, lowersAt }) => {
  const outputs: unknown[] = [];
  const c = new BufferingQualityController(
    "always",
    (output, t) => outputs.push({ t, ...output }),
    0,
    undefined,
    undefined,
    () => measured,
  );
  c.event({ type: "plan_adopted", plan }, 0);
  for (let t = 0; t <= 60000; t += 100) {
    for (const [at, type] of events) if (at === t) c.event({ type }, t);
    c.tick(t);
  }
  expect(outputs).toEqual(
    lowersAt === undefined
      ? []
      : [
          {
            t: lowersAt,
            type: "replan",
            body: {
              operation: "quality_change",
              quality_preference: "auto",
              bandwidth_estimate_kbps: 44100,
              bandwidth_estimate_source: "observed_rebuffer",
            },
          },
        ],
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
