import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ThemeMusic,
  resetThemeAudioFormats,
  themeAudioFormats,
  type ThemeGrant,
} from "./themeMusic";

function audio() {
  const element = {
    src: "",
    volume: 0,
    loop: false,
    paused: true,
    preload: "",
    onerror: null,
    onended: null,
    currentTime: 0,
    play: vi.fn(async () => {
      element.paused = false;
    }),
    pause: vi.fn(() => {
      element.paused = true;
    }),
    removeAttribute: vi.fn(() => {
      element.src = "";
    }),
    load: vi.fn(),
  };
  return element as unknown as HTMLAudioElement;
}

const selection = { owner_id: "series", items: [{ id: "1" }] };
const flush = async () => {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
};

afterEach(() => {
  vi.useRealTimers();
});

describe("ThemeMusic", () => {
  it("aborts a pending grant while suspended and reloads only after selection resumes", async () => {
    let resolve!: (grant: ThemeGrant) => void;
    const grant = vi
      .fn<(_owner: string, _theme: string, signal: AbortSignal) => Promise<ThemeGrant>>()
      .mockImplementationOnce(
        () =>
          new Promise<ThemeGrant>((done) => {
            resolve = done;
          }),
      )
      .mockResolvedValue({ url: "/audio?token=current" });
    const element = audio();
    const create = vi.fn(() => element);
    const music = new ThemeMusic(grant, create);
    music.select(selection, false);
    music.suspend();
    expect(grant.mock.calls[0]?.[2].aborted).toBe(true);
    resolve({ url: "/audio?token=obsolete" });
    await flush();
    expect(create).not.toHaveBeenCalled();
    expect(grant).toHaveBeenCalledTimes(1);
    music.select(selection, false);
    await flush();
    expect(grant).toHaveBeenCalledTimes(2);
    expect(element.src).toBe("/audio?token=current");
    expect(element.play).toHaveBeenCalledOnce();
    music.stop(true);
  });

  it("does not restart stale audio while navigation is unresolved", async () => {
    vi.useFakeTimers();
    const first = audio();
    const second = audio();
    const grant = vi.fn(async () => ({ url: "/audio" }));
    const music = new ThemeMusic(
      grant,
      vi.fn().mockReturnValueOnce(first).mockReturnValueOnce(second),
    );
    music.select(selection, false);
    await flush();
    music.suspend();
    first.onerror?.(new Event("error"));
    await flush();
    expect(grant).toHaveBeenCalledTimes(1);
    expect(first.paused).toBe(true);
    music.select(selection, false);
    await flush();
    expect(second.play).toHaveBeenCalledOnce();
    music.stop(true);
  });

  it("pauses a play promise that settles during navigation", async () => {
    vi.useFakeTimers();
    const element = audio();
    let finish!: () => void;
    vi.mocked(element.play).mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    const music = new ThemeMusic(
      async () => ({ url: "/audio" }),
      () => element,
    );
    music.select(selection, false);
    await flush();
    music.suspend();
    finish();
    await flush();
    vi.advanceTimersByTime(300);
    expect(element.paused).toBe(true);
    expect(element.volume).toBe(0);
    music.stop(true);
  });
  it("keeps one element for inherited owners and updates looping without restarting", async () => {
    vi.useFakeTimers();
    const element = audio();
    const create = vi.fn(() => element);
    const grant = vi.fn(async () => ({ url: "/audio?token=short-lived" }));
    const music = new ThemeMusic(grant, create);
    music.select(selection, false);
    await flush();
    vi.advanceTimersByTime(300);
    expect(element.volume).toBeCloseTo(0.35);
    music.select({ ...selection }, true);
    await flush();
    expect(element.loop).toBe(true);
    expect(create).toHaveBeenCalledTimes(1);
    expect(grant).toHaveBeenCalledTimes(1);
    music.stop();
    vi.advanceTimersByTime(300);
    expect(element.pause).toHaveBeenCalled();
    expect(element.src).toBe("");
  });

  it("discards a grant that arrives after navigation or profile change", async () => {
    let resolve!: (grant: ThemeGrant) => void;
    const grant = vi.fn(
      () =>
        new Promise<ThemeGrant>((done) => {
          resolve = done;
        }),
    );
    const create = vi.fn(audio);
    const music = new ThemeMusic(grant, create);
    music.select(selection, false);
    music.stop(true);
    resolve({ url: "/audio?token=obsolete" });
    await flush();
    expect(create).not.toHaveBeenCalled();
  });

  it("recovers once from a stale playback URL and then stops retrying", async () => {
    const first = audio(),
      second = audio();
    const grant = vi.fn(async () => ({ url: "/audio?token=renewed" }));
    const music = new ThemeMusic(
      grant,
      vi.fn().mockReturnValueOnce(first).mockReturnValueOnce(second),
    );
    music.select(selection, false);
    await flush();
    first.onerror?.(new Event("error"));
    await flush();
    second.onerror?.(new Event("error"));
    await flush();
    expect(grant).toHaveBeenCalledTimes(2);
    expect(second.src).toBe("");
    music.stop(true);
  });

  it("handles autoplay rejection and removes the gesture listener on stop", async () => {
    const element = audio();
    vi.mocked(element.play).mockRejectedValueOnce(new DOMException("blocked", "NotAllowedError"));
    const music = new ThemeMusic(
      async () => ({ url: "/audio" }),
      () => element,
    );
    music.select(selection, false);
    await flush();
    document.dispatchEvent(new Event("pointerdown"));
    await flush();
    expect(element.play).toHaveBeenCalledTimes(2);
    music.stop(true);
    document.dispatchEvent(new Event("keydown"));
    await flush();
    expect(element.play).toHaveBeenCalledTimes(2);
  });

  it("can renew again after recovered playback makes progress", async () => {
    const elements = [audio(), audio(), audio()];
    const grant = vi.fn(async () => ({ url: "/audio?token=renewed" }));
    const music = new ThemeMusic(grant, () => elements.shift()!);
    const first = elements[0]!;
    const second = elements[1]!;
    music.select(selection, true);
    await flush();
    first.onerror?.(new Event("error"));
    await flush();
    second.currentTime = 2;
    second.ontimeupdate?.call(second, new Event("timeupdate"));
    second.onerror?.(new Event("error"));
    await flush();
    expect(grant).toHaveBeenCalledTimes(3);
    music.stop(true);
  });

  it("replays a converted theme with a fresh grant instead of looping it", async () => {
    const first = audio();
    const second = audio();
    const grant = vi.fn(async () => ({
      url: "/stream/theme/signed",
      delivery: "converted" as const,
    }));
    const music = new ThemeMusic(
      grant,
      vi.fn().mockReturnValueOnce(first).mockReturnValueOnce(second),
    );
    music.select(selection, true);
    await flush();
    expect(first.loop).toBe(false);
    first.currentTime = 20;
    first.onended?.(new Event("ended"));
    await flush();
    expect(grant).toHaveBeenCalledTimes(2);
    expect(second.src).toBe("/stream/theme/signed");
    expect(first.src).toBe("");
    music.stop(true);
  });

  it("stops replaying a converted theme that ends without playing", async () => {
    const elements = [audio(), audio(), audio()];
    const grant = vi.fn(async () => ({
      url: "/stream/theme/signed",
      delivery: "converted" as const,
    }));
    const music = new ThemeMusic(grant, () => elements.shift()!);
    const first = elements[0]!;
    const second = elements[1]!;
    music.select(selection, true);
    await flush();
    first.onended?.(new Event("ended"));
    await flush();
    second.onended?.(new Event("ended"));
    await flush();
    expect(grant).toHaveBeenCalledTimes(2);
    music.stop(true);
  });

  it("keeps native looping for original audio and does not replay on end", async () => {
    const element = audio();
    const grant = vi.fn(async () => ({ url: "/audio", delivery: "original" as const }));
    const music = new ThemeMusic(grant, () => element);
    music.select(selection, true);
    await flush();
    expect(element.loop).toBe(true);
    element.onended?.(new Event("ended"));
    await flush();
    expect(grant).toHaveBeenCalledOnce();
    music.stop(true);
  });
});

describe("themeAudioFormats", () => {
  afterEach(() => resetThemeAudioFormats());

  it("reports the probed formats once", () => {
    const canPlayType = vi.fn((mime: string) =>
      mime.startsWith("audio/mp4") && !mime.includes("alac")
        ? "maybe"
        : mime === "audio/flac"
          ? "probably"
          : "",
    );
    const probe = vi.fn(() => ({ canPlayType }) as Pick<HTMLMediaElement, "canPlayType">);
    const formats = themeAudioFormats(probe);
    expect(formats).toEqual([
      { container: "mp4", audio_codec: "aac" },
      { container: "m4a", audio_codec: "aac" },
      { container: "flac", audio_codec: "flac" },
    ]);
    expect(themeAudioFormats(probe)).toBe(formats);
    expect(probe).toHaveBeenCalledOnce();
  });
});
