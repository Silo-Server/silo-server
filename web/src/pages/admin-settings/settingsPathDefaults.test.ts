import { describe, expect, it } from "vitest";

import { DEFAULT_TRANSCODE_DIR, effectiveDownloadArtifactDir } from "./settingsPathDefaults";

// Expectations captured from `config.EffectiveDownloadArtifactDir`
// (internal/config/config.go) run over the same inputs. This file is the only
// thing keeping the placeholder honest, so it asserts the awkward cases too.
describe("effectiveDownloadArtifactDir", () => {
  it("keeps a configured directory as typed", () => {
    expect(effectiveDownloadArtifactDir("/mnt/downloads", "/mnt/fast/transcode")).toBe(
      "/mnt/downloads",
    );
  });

  it("derives a sibling of the default transcode dir when both are blank", () => {
    expect(effectiveDownloadArtifactDir("", "")).toBe("/tmp/silo-download-artifacts");
    expect(effectiveDownloadArtifactDir("", DEFAULT_TRANSCODE_DIR)).toBe(
      "/tmp/silo-download-artifacts",
    );
  });

  it("follows a custom transcode dir", () => {
    expect(effectiveDownloadArtifactDir("", "/mnt/fast/transcode")).toBe(
      "/mnt/fast/silo-download-artifacts",
    );
    expect(effectiveDownloadArtifactDir("", "/transcode")).toBe("/silo-download-artifacts");
  });

  it("still derives the sibling for a trailing-slash transcode dir", () => {
    // Nesting inside the transcode root would put prepared downloads where the
    // orphaned-transcode sweep deletes them; the server cleans the path first.
    expect(effectiveDownloadArtifactDir("", "/mnt/fast/transcode/")).toBe(
      "/mnt/fast/silo-download-artifacts",
    );
    expect(effectiveDownloadArtifactDir("", "/")).toBe("/silo-download-artifacts");
  });

  it("cleans the derived path the way filepath.Join does", () => {
    expect(effectiveDownloadArtifactDir("", "/mnt//fast/transcode")).toBe(
      "/mnt/fast/silo-download-artifacts",
    );
    expect(effectiveDownloadArtifactDir("", "/mnt/fast/../transcode")).toBe(
      "/mnt/silo-download-artifacts",
    );
  });

  it("stays relative for relative input, like the server would", () => {
    expect(effectiveDownloadArtifactDir("", "relative/transcode")).toBe(
      "relative/silo-download-artifacts",
    );
    expect(effectiveDownloadArtifactDir("", "transcode")).toBe("silo-download-artifacts");
  });
});
