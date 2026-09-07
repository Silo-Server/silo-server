import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SubtitleTranslateModal } from "./SubtitleTranslateModal";

afterEach(cleanup);
const props = {
  mediaFileId: 7,
  playerConfig: {
    apiBaseUrl: "/api/v1",
    getAccessToken: () => null,
    getProfileId: () => null,
    getDeviceId: () => "test-device",
  },
  tracks: [
    {
      index: 0,
      language: "en",
      label: "English",
      url: "/subtitles/1",
      codec: "srt",
      source: "external" as const,
    },
  ],
  isOpen: true,
  onClose: vi.fn(),
};
it.each([
  ["hr", "hr"],
  ["hrv", "hr"],
  ["pt-BR", "pt"],
  [null, "en"],
  ["original", "en"],
  ["invalid-language", "en"],
])("initializes target from preference %s", (preferredSubtitleLanguage, expected) => {
  render(
    <SubtitleTranslateModal {...props} preferredSubtitleLanguage={preferredSubtitleLanguage} />,
  );
  expect(screen.getByRole("combobox", { name: "Translate to" })).toHaveValue(expected);
});
it("keeps a one-off choice local and restores the profile default on reopening", () => {
  const view = render(<SubtitleTranslateModal {...props} preferredSubtitleLanguage="hr" />);
  fireEvent.change(screen.getByRole("combobox", { name: "Translate to" }), {
    target: { value: "fr" },
  });
  expect(screen.getByRole("combobox", { name: "Translate to" })).toHaveValue("fr");
  view.unmount();
  render(<SubtitleTranslateModal {...props} preferredSubtitleLanguage="hr" />);
  expect(screen.getByRole("combobox", { name: "Translate to" })).toHaveValue("hr");
});
