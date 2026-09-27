import { fireEvent, render, screen } from "@testing-library/react";
import { expect, it, vi } from "vitest";
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
