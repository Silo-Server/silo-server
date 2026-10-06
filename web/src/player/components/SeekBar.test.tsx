import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { SeekBar } from "./SeekBar";

it("uses directional callbacks for arrows while Home and End remain absolute seeks", () => {
  const back = vi.fn(),
    forward = vi.fn(),
    seek = vi.fn();
  render(
    <SeekBar
      currentTime={50}
      duration={120}
      buffered={null}
      onSeek={seek}
      onSkip={{ back, forward }}
    />,
  );
  const slider = screen.getByRole("slider");
  fireEvent.keyDown(slider, { key: "ArrowLeft" });
  fireEvent.keyDown(slider, { key: "ArrowRight" });
  expect(back).toHaveBeenCalledOnce();
  expect(forward).toHaveBeenCalledOnce();
  expect(seek).not.toHaveBeenCalled();
  fireEvent.keyDown(slider, { key: "Home" });
  expect(seek).toHaveBeenLastCalledWith(0);
  fireEvent.keyDown(slider, { key: "End" });
  expect(seek).toHaveBeenLastCalledWith(120);
});

it("keeps a 5s nudge on Shift+Arrow and leaves modifier shortcuts alone", () => {
  const back = vi.fn(),
    forward = vi.fn(),
    seek = vi.fn();
  render(
    <SeekBar
      currentTime={50}
      duration={120}
      buffered={null}
      onSeek={seek}
      onSkip={{ back, forward }}
    />,
  );
  const slider = screen.getByRole("slider");
  fireEvent.keyDown(slider, { key: "ArrowRight", shiftKey: true });
  expect(seek).toHaveBeenLastCalledWith(55);
  fireEvent.keyDown(slider, { key: "ArrowLeft", shiftKey: true });
  expect(seek).toHaveBeenLastCalledWith(45);
  fireEvent.keyDown(slider, { key: "ArrowLeft", metaKey: true });
  fireEvent.keyDown(slider, { key: "ArrowRight", ctrlKey: true });
  expect(back).not.toHaveBeenCalled();
  expect(forward).not.toHaveBeenCalled();
  expect(seek).toHaveBeenCalledTimes(2);
});

const trickplay = {
  intervalMs: 10_000,
  width: 300,
  height: 126,
  columns: 10,
  rows: 10,
  count: 120,
  sheets: ["https://cdn.example/sheet-0.jpg", "https://cdn.example/sheet-1.jpg"],
  expiresAt: Date.now() + 3_600_000,
};

// jsdom loads no images. Each sheet settles as a test says: loads by
// default, fails, or stays pending.
const sheetOutcomes = new Map<string, "error" | "pending">();

class FakeImage {
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  set src(url: string) {
    const outcome = sheetOutcomes.get(url);
    if (outcome === "pending") return;
    queueMicrotask(() => (outcome === "error" ? this.onerror : this.onload)?.());
  }
}

beforeEach(() => {
  sheetOutcomes.clear();
  vi.stubGlobal("Image", FakeImage);
});
afterEach(() => vi.unstubAllGlobals());

function renderSeekBar(props: Partial<Parameters<typeof SeekBar>[0]> = {}) {
  const view = render(
    <SeekBar
      currentTime={0}
      duration={1200}
      buffered={null}
      onSeek={vi.fn()}
      onSkip={{ back: vi.fn(), forward: vi.fn() }}
      {...props}
    />,
  );
  // jsdom lays nothing out; give the bar a 1000px track.
  for (const element of view.container.querySelectorAll("div")) {
    element.getBoundingClientRect = () =>
      ({ left: 0, width: 1000, top: 0, height: 10, right: 1000, bottom: 10 }) as DOMRect;
  }
  return view;
}

const chapters = [
  {
    index: 0,
    title: "Opening",
    start_seconds: 0,
    end_seconds: 1200,
    source: "embedded" as const,
    thumbnail_url: "https://cdn.example/chapter.webp",
  },
];

it("shows the seek preview sprite for the hovered time", async () => {
  renderSeekBar({ trickplay });
  // 250px of 1000 is 300 s: thumbnail 30, sheet 0, column 0, row 3.
  fireEvent.mouseMove(screen.getByRole("slider"), { clientX: 250 });
  const image = await screen.findByTestId("seek-preview-image");
  expect(image.style.backgroundImage).toContain("sheet-0.jpg");
  expect(image.style.backgroundSize).toBe("1000% 1000%");
  expect(image.style.backgroundPosition).toMatch(/^0% 33\.33/);
  expect(screen.getByLabelText("Preview at 5:00")).toBeTruthy();
});

it("keeps the preview while dragging and on touch", async () => {
  renderSeekBar({ trickplay });
  const slider = screen.getByRole("slider");
  fireEvent.mouseDown(slider, { clientX: 900 });
  fireEvent.mouseMove(document, { clientX: 950 });
  // 950px is 1140 s: past the last thumbnail (119), on sheet 1.
  expect((await screen.findByTestId("seek-preview-image")).style.backgroundImage).toContain(
    "sheet-1.jpg",
  );
  fireEvent.mouseUp(document, { clientX: 950 });
  expect(screen.queryByTestId("seek-preview")).toBeNull();

  fireEvent.touchStart(slider, { touches: [{ clientX: 100 }] });
  expect((await screen.findByTestId("seek-preview-image")).style.backgroundImage).toContain(
    "sheet-0.jpg",
  );
});

it("falls back to the chapter thumbnail without previews", () => {
  renderSeekBar({ chapters });
  fireEvent.mouseMove(screen.getByRole("slider"), { clientX: 250 });
  expect(screen.queryByTestId("seek-preview-image")).toBeNull();
  expect((screen.getByAltText("Opening") as HTMLImageElement).src).toContain("chapter.webp");
});

it("shows the chapter thumbnail until the sheet loads", async () => {
  sheetOutcomes.set("https://cdn.example/sheet-0.jpg", "pending");
  renderSeekBar({ trickplay, chapters });
  fireEvent.mouseMove(screen.getByRole("slider"), { clientX: 250 });
  await act(async () => {});
  expect(screen.queryByTestId("seek-preview-image")).toBeNull();
  expect(screen.getByAltText("Opening")).toBeTruthy();
});

it("restores a failed sheet when the same URL later loads", async () => {
  sheetOutcomes.set(trickplay.sheets[0]!, "error");
  const onTrickplayError = vi.fn();
  renderSeekBar({ trickplay, chapters, onTrickplayError });
  const slider = screen.getByRole("slider");
  fireEvent.mouseMove(slider, { clientX: 250 });
  await act(async () => {});
  expect(screen.queryByTestId("seek-preview-image")).toBeNull();
  expect(onTrickplayError).toHaveBeenCalledOnce();
  expect(screen.getByAltText("Opening")).toBeTruthy();

  // Moving to another sheet and back preloads the unchanged manifest URL.
  sheetOutcomes.delete(trickplay.sheets[0]!);
  fireEvent.mouseMove(slider, { clientX: 950 });
  await screen.findByTestId("seek-preview-image");
  fireEvent.mouseMove(slider, { clientX: 250 });
  await act(async () => {});
  expect(screen.getByTestId("seek-preview-image").style.backgroundImage).toContain("sheet-0.jpg");
});
