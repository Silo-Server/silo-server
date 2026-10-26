import { describe, expect, it } from "vitest";

import { hasMachineTranslation } from "./machineTranslation";

describe("hasMachineTranslation", () => {
  it("names only the field it is asked about", () => {
    // A provider overview with an AI tagline must not label the overview.
    expect(hasMachineTranslation(["tagline"])).toBe(false);
    expect(hasMachineTranslation(["tagline"], "tagline")).toBe(true);
    expect(hasMachineTranslation(["overview", "tagline"])).toBe(true);
    expect(hasMachineTranslation(["overview"], "tagline")).toBe(false);
    expect(hasMachineTranslation(undefined)).toBe(false);
  });
});
