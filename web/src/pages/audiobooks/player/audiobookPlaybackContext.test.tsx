import { fireEvent, render, screen, act, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { AudiobookPlayerProps } from "./AudiobookPlayer";
import {
  AudiobookPlaybackProvider,
  useAudiobookPlaybackController,
} from "./audiobookPlaybackContext";

const playerModule = vi.hoisted(() => {
  let resolve!: () => void;
  return {
    requested: vi.fn(),
    ready: new Promise<void>((done) => {
      resolve = done;
    }),
    resolve: () => resolve(),
  };
});

vi.mock("./AudiobookPlayer", async () => {
  playerModule.requested();
  await playerModule.ready;
  return {
    default: ({ title, onClose }: AudiobookPlayerProps) => (
      <div aria-label="Audiobook player">
        {title}
        <button onClick={onClose}>Close player</button>
      </div>
    ),
  };
});

function PlaybackControls() {
  const playback = useAudiobookPlaybackController()!;
  return (
    <>
      <input aria-label="Library search" defaultValue="Unchanged page" />
      <button
        onClick={() =>
          playback.startPlayback({ contentId: "book-1", title: "First book", files: [] })
        }
      >
        Start audiobook
      </button>
      <button onClick={playback.stopPlayback}>Stop audiobook</button>
    </>
  );
}

it("loads the player on demand, preserves the page while pending, and honors cancellation", async () => {
  render(
    <AudiobookPlaybackProvider>
      <PlaybackControls />
    </AudiobookPlaybackProvider>,
  );
  const search = screen.getByRole("textbox", { name: "Library search" });
  fireEvent.change(search, { target: { value: "My search" } });
  expect(playerModule.requested).not.toHaveBeenCalled();

  fireEvent.click(screen.getByRole("button", { name: "Start audiobook" }));
  await waitFor(() => expect(playerModule.requested).toHaveBeenCalledOnce());
  expect(search).toBeVisible();
  expect(search).toHaveValue("My search");
  expect(screen.queryByLabelText("Audiobook player")).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole("button", { name: "Stop audiobook" }));
  await act(async () => playerModule.resolve());
  expect(screen.queryByLabelText("Audiobook player")).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole("button", { name: "Start audiobook" }));
  expect(await screen.findByLabelText("Audiobook player")).toHaveTextContent("First book");
  expect(screen.getByRole("textbox", { name: "Library search" })).toBe(search);
  expect(search).toHaveValue("My search");
  fireEvent.click(screen.getByRole("button", { name: "Close player" }));
  expect(screen.queryByLabelText("Audiobook player")).not.toBeInTheDocument();
});
