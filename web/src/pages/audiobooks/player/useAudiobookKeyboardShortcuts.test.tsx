import { describe, expect, it, vi } from "vitest";
import { fireEvent, renderHook } from "@testing-library/react";
import { useAudiobookKeyboardShortcuts } from "./useAudiobookKeyboardShortcuts";
import { makePlayback, makePrefs } from "./playerTestUtils";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

function setup(playbackOver: Partial<AudiobookPlayback> = {}, expanded = false) {
  const playback = makePlayback(playbackOver);
  const onToggleExpanded = vi.fn();
  const onCollapse = vi.fn();
  renderHook(() =>
    useAudiobookKeyboardShortcuts({
      playback,
      prefs: makePrefs({ skipBack: 15, skipForward: 45 }),
      expanded,
      onToggleExpanded,
      onCollapse,
    }),
  );
  return { playback, onToggleExpanded, onCollapse };
}

describe("useAudiobookKeyboardShortcuts", () => {
  it("toggles playback on space and K", () => {
    const { playback } = setup();
    fireEvent.keyDown(document.body, { key: " " });
    fireEvent.keyDown(document.body, { key: "k" });
    expect(playback.togglePlay).toHaveBeenCalledTimes(2);
  });

  it("skips by the configured intervals on arrow keys", () => {
    const { playback } = setup();
    fireEvent.keyDown(document.body, { key: "ArrowLeft" });
    expect(playback.skip).toHaveBeenCalledWith(-15);
    fireEvent.keyDown(document.body, { key: "ArrowRight" });
    expect(playback.skip).toHaveBeenCalledWith(45);
  });

  it("adjusts volume on up/down and unmutes when raising while muted", () => {
    const { playback } = setup({ volume: 0.5, muted: true });
    fireEvent.keyDown(document.body, { key: "ArrowUp" });
    expect(playback.setVolume).toHaveBeenCalledWith(0.55);
    expect(playback.setMuted).toHaveBeenCalledWith(false);
    fireEvent.keyDown(document.body, { key: "ArrowDown" });
    expect(playback.setVolume).toHaveBeenCalledWith(0.45);
  });

  it("toggles mute on M and navigates chapters on N/P", () => {
    const { playback } = setup();
    fireEvent.keyDown(document.body, { key: "m" });
    expect(playback.setMuted).toHaveBeenCalledWith(true);
    fireEvent.keyDown(document.body, { key: "n" });
    expect(playback.nextChapter).toHaveBeenCalled();
    fireEvent.keyDown(document.body, { key: "p" });
    expect(playback.prevChapter).toHaveBeenCalled();
  });

  it("steps the playback rate on shift+. and shift+,", () => {
    const { playback } = setup({ rate: 1.5 });
    fireEvent.keyDown(document.body, { key: ">", shiftKey: true });
    expect(playback.setRate).toHaveBeenCalledWith(1.55);
    fireEvent.keyDown(document.body, { key: "<", shiftKey: true });
    expect(playback.setRate).toHaveBeenCalledWith(1.45);
  });

  it("expands/collapses on E and collapses on Escape when expanded", () => {
    const { onToggleExpanded, onCollapse } = setup({}, true);
    fireEvent.keyDown(document.body, { key: "e" });
    expect(onToggleExpanded).toHaveBeenCalled();
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(onCollapse).toHaveBeenCalled();
  });

  it("ignores Escape when collapsed", () => {
    const { onCollapse } = setup({}, false);
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(onCollapse).not.toHaveBeenCalled();
  });

  it("does nothing while typing in an input", () => {
    const { playback } = setup();
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    fireEvent.keyDown(input, { key: " " });
    expect(playback.togglePlay).not.toHaveBeenCalled();
    input.remove();
  });

  it("leaves space alone when a button has focus", () => {
    const { playback } = setup();
    const button = document.createElement("button");
    document.body.appendChild(button);
    button.focus();
    fireEvent.keyDown(button, { key: " " });
    expect(playback.togglePlay).not.toHaveBeenCalled();
    button.remove();
  });

  it("ignores keys with system modifiers held", () => {
    const { playback } = setup();
    fireEvent.keyDown(document.body, { key: "k", metaKey: true });
    fireEvent.keyDown(document.body, { key: "ArrowLeft", ctrlKey: true });
    expect(playback.togglePlay).not.toHaveBeenCalled();
    expect(playback.skip).not.toHaveBeenCalled();
  });

  it("stays inert without a loaded file", () => {
    const { playback } = setup({ hasFile: false });
    fireEvent.keyDown(document.body, { key: " " });
    expect(playback.togglePlay).not.toHaveBeenCalled();
  });
});
