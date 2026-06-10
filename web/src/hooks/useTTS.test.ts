// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useTTS } from "./useTTS";

class FakeUtterance {
  text: string;
  rate = 1;
  pitch = 1;
  volume = 1;
  lang = "";
  voice: SpeechSynthesisVoice | null = null;
  onend: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(text: string) {
    this.text = text;
  }
}

type FakeSynth = {
  speak: ReturnType<typeof vi.fn>;
  cancel: ReturnType<typeof vi.fn>;
  pause: ReturnType<typeof vi.fn>;
  resume: ReturnType<typeof vi.fn>;
  getVoices: ReturnType<typeof vi.fn>;
  addEventListener: ReturnType<typeof vi.fn>;
  removeEventListener: ReturnType<typeof vi.fn>;
  spoken: FakeUtterance[];
};

// Browsers dispatch a synthetic event on the in-flight utterance when cancel()
// is called: Chromium fires end, Firefox fires error.
function installFakeSpeechSynthesis(cancelEvent: "end" | "error"): FakeSynth {
  const spoken: FakeUtterance[] = [];
  let inFlight: FakeUtterance | null = null;
  const synth: FakeSynth = {
    spoken,
    speak: vi.fn((utterance: FakeUtterance) => {
      inFlight = utterance;
      spoken.push(utterance);
    }),
    cancel: vi.fn(() => {
      const utterance = inFlight;
      inFlight = null;
      if (!utterance) return;
      if (cancelEvent === "end") utterance.onend?.();
      else utterance.onerror?.();
    }),
    pause: vi.fn(),
    resume: vi.fn(),
    getVoices: vi.fn(() => []),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  };
  vi.stubGlobal("speechSynthesis", synth);
  vi.stubGlobal("SpeechSynthesisUtterance", FakeUtterance);
  return synth;
}

type FakeMediaSession = {
  metadata: unknown;
  playbackState: string;
  setActionHandler: ReturnType<typeof vi.fn>;
};

function installFakeMediaSession(): FakeMediaSession {
  const mediaSession: FakeMediaSession = {
    metadata: null,
    playbackState: "none",
    setActionHandler: vi.fn(),
  };
  Object.defineProperty(navigator, "mediaSession", {
    value: mediaSession,
    configurable: true,
  });
  vi.stubGlobal(
    "MediaMetadata",
    class {
      title: string;
      constructor(init: { title?: string } = {}) {
        this.title = init.title ?? "";
      }
    },
  );
  return mediaSession;
}

// Long enough to split into multiple queue chunks.
const MULTI_CHUNK_TEXT = "First sentence here. Second sentence here. Third sentence here.";

describe("useTTS", () => {
  beforeEach(() => {
    installFakeMediaSession();
  });

  afterEach(() => {
    // Unmount while the fake speechSynthesis is still installed; the hook's
    // effect cleanup dereferences window.speechSynthesis.
    cleanup();
    vi.unstubAllGlobals();
  });

  it("speaks chunks sequentially and returns to idle when the queue drains", () => {
    const synth = installFakeSpeechSynthesis("end");
    const { result } = renderHook(() => useTTS());

    act(() => {
      result.current.speak("One. Two.");
    });
    expect(result.current.state).toBe("speaking");
    expect(synth.spoken).toHaveLength(1);
    expect(synth.spoken[0]!.text).toBe("One.");

    act(() => {
      synth.spoken[0]!.onend?.();
    });
    expect(synth.spoken).toHaveLength(2);
    expect(synth.spoken[1]!.text).toBe("Two.");

    act(() => {
      synth.spoken[1]!.onend?.();
    });
    expect(result.current.state).toBe("idle");
    expect(synth.spoken).toHaveLength(2);
  });

  it("stop stays stopped when cancel dispatches end on the in-flight utterance (Chromium)", () => {
    const synth = installFakeSpeechSynthesis("end");
    const { result } = renderHook(() => useTTS());

    act(() => {
      result.current.speak(MULTI_CHUNK_TEXT);
    });
    expect(synth.spoken).toHaveLength(1);
    // Capture the handler as the browser's event loop would have: an end event
    // already dispatched for the in-flight utterance may race the cancel.
    const racingHandler = synth.spoken[0]!.onend;

    act(() => {
      result.current.stop();
      racingHandler?.();
    });

    expect(synth.cancel).toHaveBeenCalled();
    expect(synth.spoken).toHaveLength(1);
    expect(result.current.state).toBe("idle");
  });

  it("stop stays stopped when cancel dispatches error on the in-flight utterance (Firefox)", () => {
    const synth = installFakeSpeechSynthesis("error");
    const { result } = renderHook(() => useTTS());

    act(() => {
      result.current.speak(MULTI_CHUNK_TEXT);
    });
    const racingHandler = synth.spoken[0]!.onerror;

    act(() => {
      result.current.stop();
      racingHandler?.();
    });

    expect(synth.spoken).toHaveLength(1);
    expect(result.current.state).toBe("idle");
  });

  it("does not interleave the previous queue when a new speak starts", () => {
    const synth = installFakeSpeechSynthesis("end");
    const { result } = renderHook(() => useTTS());

    act(() => {
      result.current.speak(MULTI_CHUNK_TEXT);
    });
    const staleHandler = synth.spoken[0]!.onend;

    act(() => {
      result.current.speak("New text. More new text.");
      // A late end event from the superseded utterance must not advance the
      // old queue alongside the new one.
      staleHandler?.();
    });

    expect(synth.spoken.map((utterance) => utterance.text)).toEqual([
      "First sentence here.",
      "New text.",
    ]);

    act(() => {
      synth.spoken[1]!.onend?.();
    });
    expect(synth.spoken.map((utterance) => utterance.text)).toEqual([
      "First sentence here.",
      "New text.",
      "More new text.",
    ]);
  });

  it("installs media session handlers on speak and clears them on stop", () => {
    installFakeSpeechSynthesis("end");
    const mediaSession = navigator.mediaSession as unknown as FakeMediaSession;
    const { result } = renderHook(() => useTTS());

    act(() => {
      result.current.speak("Hello there.");
    });
    expect(mediaSession.metadata).not.toBeNull();
    expect(mediaSession.playbackState).toBe("playing");

    act(() => {
      result.current.stop();
    });
    expect(mediaSession.metadata).toBeNull();
    expect(mediaSession.playbackState).toBe("none");
    expect(mediaSession.setActionHandler).toHaveBeenCalledWith("play", null);
    expect(mediaSession.setActionHandler).toHaveBeenCalledWith("pause", null);
    expect(mediaSession.setActionHandler).toHaveBeenCalledWith("stop", null);
  });

  it("cancels speech and uninstalls the media session when unmounting mid-speech", () => {
    const synth = installFakeSpeechSynthesis("end");
    const mediaSession = navigator.mediaSession as unknown as FakeMediaSession;
    const { result, unmount } = renderHook(() => useTTS());

    act(() => {
      result.current.speak(MULTI_CHUNK_TEXT);
    });
    expect(mediaSession.playbackState).toBe("playing");

    unmount();

    expect(synth.cancel).toHaveBeenCalled();
    // No chunk may start after unmount, even though cancel fired an end event.
    expect(synth.spoken).toHaveLength(1);
    expect(mediaSession.metadata).toBeNull();
    expect(mediaSession.playbackState).toBe("none");
    expect(mediaSession.setActionHandler).toHaveBeenCalledWith("play", null);
    expect(mediaSession.setActionHandler).toHaveBeenCalledWith("pause", null);
    expect(mediaSession.setActionHandler).toHaveBeenCalledWith("stop", null);
  });
});
