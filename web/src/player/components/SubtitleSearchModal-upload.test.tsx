import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SubtitleSearchModal } from "./SubtitleSearchModal";
import type { PlayerConfig } from "../context/PlayerConfigContext";

const mocks = vi.hoisted(() => ({ v2: vi.fn() }));
vi.mock("../player-v2", () => ({ playerV2: mocks.v2 }));
afterEach(() => {
  cleanup();
  mocks.v2.mockReset();
});

const refusal =
  "This file doesn't contain readable subtitles. Upload an SRT, VTT, ASS, SSA or SUB subtitle file.";

// A refused upload is reported once, next to the upload form, and not again
// in the online-search area where it would read as a search failure.
it("shows a refused upload once, beside the upload form", async () => {
  const config: PlayerConfig = {
    apiBaseUrl: "/api/v1",
    getAccessToken: () => "synthetic",
    getProfileId: () => "profile-1",
    getDeviceId: () => "synthetic-device",
  };
  mocks.v2.mockImplementation(async (_config: PlayerConfig, operation: string) => {
    if (operation === "POST /api/v2/subtitles/detect-language")
      return { language: "en", source: "manual" };
    if (operation === "POST /api/v2/subtitles/upload") throw new Error(refusal);
    throw new Error(`unexpected ${operation}`);
  });
  render(
    <SubtitleSearchModal
      playerConfig={config}
      mediaFileId={42}
      isOpen
      onSubtitleDownloaded={vi.fn()}
      onClose={vi.fn()}
    />,
  );

  const input = document.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error("upload input missing");
  fireEvent.change(input, { target: { files: [new File(["not subtitles"], "notes.srt")] } });
  fireEvent.click(await screen.findByRole("button", { name: /^Upload$/ }));

  expect(await screen.findByText(refusal)).toBeTruthy();
  expect(screen.getAllByText(refusal)).toHaveLength(1);
});
