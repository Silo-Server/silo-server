import { describe, expect, it } from "vitest";
import vectors from "../../../docs/design/schemas/buffering-quality/v1/vectors.json";
import {
  BufferingQualityController,
  type BufferingEvent,
  type BufferingMode,
} from "./buffering-quality";

describe("buffering-quality wire vectors", () => {
  for (const vector of vectors.cases)
    it(vector.name, () => {
      const outputs: unknown[] = [];
      const c = new BufferingQualityController(
        ("mode" in vector ? vector.mode : vectors.defaults.mode) as BufferingMode,
        (output, t) => outputs.push({ t, ...output }),
        0,
      );
      let clock = 0;
      const advance = (to: number) => {
        while (clock < to) {
          clock = Math.min(to, clock + 100);
          c.tick(clock);
        }
      };
      for (const event of vector.events) {
        advance(event.t);
        c.event(event as BufferingEvent, event.t);
      }
      advance(vector.until);
      expect(outputs).toEqual(vector.expect);
      if ("buffering_action" in vector)
        for (const [id, action] of Object.entries(vector.buffering_action ?? {})) {
          expect(c.summaries.get(id)?.buffering_action).toBe(action);
        }
    });
});
