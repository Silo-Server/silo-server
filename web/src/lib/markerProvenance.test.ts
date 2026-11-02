import { describe, expect, it } from "vitest";

import { confidencePercent, markerProvenanceLabel, markerSourceLabel } from "./markerProvenance";

describe("markerSourceLabel", () => {
  it("names a plugin provider by its capability, not its installation id", () => {
    // Measured on a live server: a plugin marker stores the provider qualified
    // with the local installation id. Matching the whole string would show a
    // viewer "plugin:4:introdb".
    expect(markerSourceLabel({ source: "plugin", provider: "plugin:4:introdb" })).toBe(
      "TheIntroDB",
    );
    expect(markerSourceLabel({ source: "plugin", provider: "plugin:11:introdb" })).toBe(
      "TheIntroDB",
    );
  });

  it("maps the other sources to what they are", () => {
    expect(markerSourceLabel({ source: "scanner" })).toBe("Detected");
    expect(markerSourceLabel({ source: "manual" })).toBe("Edited");
    expect(markerSourceLabel({ source: "plugin", provider: "built_in" })).toBe("Detected");
    expect(markerSourceLabel({ source: "online", provider: "introdb" })).toBe("TheIntroDB");
  });

  it("falls back rather than hiding an unknown provider", () => {
    // The badge exists to answer "where did this come from?", so an unfamiliar
    // provider shows its own id; blank would answer nothing.
    expect(markerSourceLabel({ source: "plugin", provider: "some.other.plugin" })).toBe(
      "some.other.plugin",
    );
    expect(markerSourceLabel({ source: "online" })).toBe("Online");
  });

  it("says nothing when nothing is known", () => {
    expect(markerSourceLabel({})).toBeNull();
  });
});

describe("confidencePercent", () => {
  it("renders a whole percentage", () => {
    expect(confidencePercent({ confidence: 0.2 })).toBe("20%");
    expect(confidencePercent({ confidence: 0.15 })).toBe("15%");
    expect(confidencePercent({ confidence: 1 })).toBe("100%");
  });

  it("treats a missing confidence as nothing to show, not as 0%", () => {
    // The server sends confidence: 0 for "nobody rated this". Rendering that as a
    // real measurement would be inventing one.
    expect(confidencePercent({})).toBeNull();
    expect(confidencePercent({ confidence: null })).toBeNull();
    expect(confidencePercent({ confidence: Number.NaN })).toBeNull();
  });

  it("clamps rather than printing an impossible value", () => {
    expect(confidencePercent({ confidence: 1.5 })).toBe("100%");
    expect(confidencePercent({ confidence: -0.2 })).toBe("0%");
  });
});

describe("markerProvenanceLabel", () => {
  it("shows confidence, source, or both by setting", () => {
    const provenance = { source: "plugin", provider: "plugin:4:introdb", confidence: 0.2 };

    expect(markerProvenanceLabel(provenance, { showConfidence: true, showSource: false })).toBe(
      "20%",
    );
    expect(markerProvenanceLabel(provenance, { showConfidence: false, showSource: true })).toBe(
      "TheIntroDB",
    );
    expect(markerProvenanceLabel(provenance, { showConfidence: true, showSource: true })).toBe(
      "20% · TheIntroDB",
    );
    expect(
      markerProvenanceLabel(provenance, { showConfidence: false, showSource: false }),
    ).toBeNull();
  });

  it("degrades to the source alone for an unrated marker", () => {
    // A provider-sourced credits range was measured without a confidence, so a
    // viewer with only the confidence setting on sees the prompt unchanged.
    const unrated = { source: "plugin", provider: "plugin:4:introdb" };
    expect(markerProvenanceLabel(unrated, { showConfidence: true, showSource: false })).toBeNull();
    expect(markerProvenanceLabel(unrated, { showConfidence: true, showSource: true })).toBe(
      "TheIntroDB",
    );
  });

  it("draws nothing when nothing is known", () => {
    expect(markerProvenanceLabel({}, { showConfidence: true, showSource: true })).toBeNull();
  });
});
