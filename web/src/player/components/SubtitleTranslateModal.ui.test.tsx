import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { SubtitleTranslateModal } from "./SubtitleTranslateModal";

const playerV2Mock = vi.hoisted(() => vi.fn());
vi.mock("../player-v2", () => ({ playerV2: playerV2Mock }));
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

it("reports the validated accepted job to the player before closing", async () => {
  playerV2Mock.mockResolvedValue({
    job: { id: "11", media_file_id: "7", kind: "translate", source_index: 0 },
    live_delivery_attached: true,
  });
  const onSubtitleJobAccepted = vi.fn();
  const onClose = vi.fn();
  render(
    <SubtitleTranslateModal
      {...props}
      onSubtitleJobAccepted={onSubtitleJobAccepted}
      onClose={onClose}
    />,
  );
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Translate" }));
  });
  expect(onSubtitleJobAccepted).toHaveBeenCalledExactlyOnceWith("11", undefined);
  expect(onClose).toHaveBeenCalledOnce();
  expect(onSubtitleJobAccepted.mock.invocationCallOrder[0]).toBeLessThan(
    onClose.mock.invocationCallOrder[0]!,
  );
});

it("says when the server returns subtitles it already created", async () => {
  playerV2Mock.mockResolvedValue({
    job: {
      id: "12",
      media_file_id: "7",
      kind: "translate",
      source_index: 0,
      status: "completed",
      result_subtitle_id: "31",
    },
    live_delivery_attached: false,
  });
  const info = vi.spyOn(toast, "info").mockImplementation(() => "toast");
  const onSubtitleJobAccepted = vi.fn();
  render(<SubtitleTranslateModal {...props} onSubtitleJobAccepted={onSubtitleJobAccepted} />);
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Translate" }));
  });
  expect(info).toHaveBeenCalledExactlyOnceWith(
    "These subtitles were already created, so the saved track is selected.",
  );
  expect(onSubtitleJobAccepted).toHaveBeenCalledExactlyOnceWith("12", "31");
  info.mockRestore();
});
