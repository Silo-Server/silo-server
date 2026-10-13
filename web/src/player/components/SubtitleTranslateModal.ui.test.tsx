import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
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
  expect(onSubtitleJobAccepted).toHaveBeenCalledExactlyOnceWith("11");
  expect(onClose).toHaveBeenCalledOnce();
  expect(onSubtitleJobAccepted.mock.invocationCallOrder[0]).toBeLessThan(
    onClose.mock.invocationCallOrder[0]!,
  );
});

it("offers only the spoken language for audio when translation is off", async () => {
  playerV2Mock.mockResolvedValue({
    job: { id: "13", media_file_id: "7", kind: "transcribe", source_index: 0, status: "pending" },
    live_delivery_attached: true,
  });
  render(
    <SubtitleTranslateModal
      {...props}
      translateEnabled={false}
      transcribeEnabled
      audioTracks={[{ language: "de" }]}
    />,
  );
  expect(screen.queryByRole("combobox", { name: "Subtitle language" })).toBeNull();
  expect(screen.getByText(/Subtitles are written in the spoken language/)).toBeInTheDocument();
  expect(screen.queryByText(/translated if the language differs/)).toBeNull();
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Generate" }));
  });
  expect(playerV2Mock).toHaveBeenCalledWith(
    expect.anything(),
    "POST /api/v2/subtitles/ai/translate",
    expect.objectContaining({
      body: expect.objectContaining({ kind: "transcribe", target_language: "" }),
    }),
  );
});
