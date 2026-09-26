import { afterEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ v2: vi.fn(), capture: vi.fn(), active: vi.fn() }));
vi.mock("@/api/v2/request", () => ({ v2: mocks.v2 }));

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  captureProfileRequestContext: mocks.capture,
  isCapturedProfileAuthorityActive: mocks.active,
}));
import {
  uploadSubtitle,
  detectSubtitleLanguage,
  downloadSubtitle,
  fetchDownloadedSubtitles,
  searchSubtitles,
} from "./subtitles";

afterEach(() => mocks.v2.mockReset());

it("sends search IDs as strings and preserves cancellation and partial results", async () => {
  const signal = new AbortController().signal;
  const response = { results: [{ id: "provider-result" }], warnings: ["Provider unavailable"] };
  mocks.v2.mockResolvedValue(response);
  expect(await searchSubtitles({ media_file_id: 42, languages: ["en"] }, { signal })).toBe(
    response,
  );
  expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/subtitles/search", {
    body: { media_file_id: "42", languages: ["en"] },
    signal,
  });
});

it("adapts stored IDs only at the existing numeric track selector boundary", async () => {
  mocks.v2.mockResolvedValue({ subtitles: [{ id: "7", media_file_id: "42", language: "en" }] });
  expect(await fetchDownloadedSubtitles(42)).toEqual([
    { id: 7, media_file_id: 42, language: "en" },
  ]);
  expect(mocks.v2).toHaveBeenCalledWith("GET /api/v2/subtitles/{media_file_id}", {
    path: { media_file_id: "42" },
    signal: undefined,
  });
});

it.each(["not-numeric", "9007199254740993", "0", "-1"])(
  "refuses to silently coerce stored ID %s into a different track",
  async (id) => {
    mocks.v2.mockResolvedValue({ subtitles: [{ id, media_file_id: "42" }] });
    await expect(fetchDownloadedSubtitles(42)).rejects.toThrow("Unsupported subtitle identifier");
  },
);

const selected = {
  media_file_id: 42,
  provider: "example",
  subtitle_id: "opaque",
  language: "en",
  release_name: "Synthetic",
  format: "srt",
  score: 80,
  hearing_impaired: false,
};
it("downloads once with captured authority and refuses stale decoded completion", async () => {
  const snapshot = { profileId: "p-owner", accessToken: "synthetic" };
  mocks.capture.mockReturnValue(snapshot);
  mocks.active.mockReturnValue(true);
  mocks.v2.mockResolvedValue({ subtitle: { id: "7", media_file_id: "42" } });
  expect(await downloadSubtitle(selected)).toEqual({ subtitle: { id: 7, media_file_id: 42 } });
  expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/subtitles/download", {
    body: {
      media_file_id: "42",
      provider: "example",
      subtitle_id: "opaque",
      language: "en",
      release_name: "Synthetic",
      score: 80,
      hearing_impaired: false,
    },
    profileContext: snapshot,
    retryAuthentication: false,
    signal: undefined,
  });
  mocks.active.mockReturnValue(false);
  await expect(downloadSubtitle(selected)).rejects.toThrow();
});
it("does not repeat an uncertain download or coerce a different returned file", async () => {
  mocks.capture.mockReturnValue({ profileId: "p-owner" });
  mocks.active.mockReturnValue(true);
  mocks.v2.mockRejectedValue(new Error("lost response"));
  await expect(downloadSubtitle(selected)).rejects.toThrow("lost response");
  expect(mocks.v2).toHaveBeenCalledTimes(1);
  mocks.v2.mockResolvedValue({ subtitle: { id: "7", media_file_id: "43" } });
  await expect(downloadSubtitle(selected)).rejects.toThrow("Unsupported subtitle identifier");
});

it("uploads one captured multipart intent and keeps detection non-persistent", async () => {
  const snapshot = { profileId: "p-owner", accessToken: "synthetic" };
  mocks.capture.mockReturnValue(snapshot);
  mocks.active.mockReturnValue(true);
  const file = new File(["synthetic"], "synthetic.en.srt");
  mocks.v2.mockResolvedValue({ subtitle: { id: "7", media_file_id: "42" } });
  await uploadSubtitle({ media_file_id: 42, file, language: "fr", language_override: true });
  expect(mocks.v2).toHaveBeenLastCalledWith("POST /api/v2/subtitles/upload", {
    form: {
      media_file_id: "42",
      file,
      language: "fr",
      language_override: "true",
      release_name: undefined,
      hearing_impaired: "false",
    },
    profileContext: snapshot,
    retryAuthentication: false,
    signal: undefined,
  });
  mocks.v2.mockResolvedValue({ language: "en", source: "filename" });
  expect(await detectSubtitleLanguage(file, "fr")).toEqual({ language: "en", source: "filename" });
  expect(mocks.v2).toHaveBeenLastCalledWith("POST /api/v2/subtitles/detect-language", {
    form: { file, language: "fr" },
    profileContext: snapshot,
    signal: undefined,
  });
  mocks.active.mockReturnValue(false);
  await expect(detectSubtitleLanguage(file)).rejects.toThrow();
});
