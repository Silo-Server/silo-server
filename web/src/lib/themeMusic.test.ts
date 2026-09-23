import { afterEach, describe, expect, it, vi } from "vitest";
import { ThemeMusic } from "./themeMusic";

function audio() {
  const element = {
    src: "",
    volume: 0,
    loop: false,
    paused: true,
    preload: "",
    onerror: null,
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
    let resolve!: (url: string) => void;
    const grant = vi
      .fn<(_owner: string, _theme: string, signal: AbortSignal) => Promise<string>>()
      .mockImplementationOnce(
        () =>
          new Promise<string>((done) => {
            resolve = done;
          }),
      )
      .mockResolvedValue("/audio?token=current");
    const element = audio();
    const create = vi.fn(() => element);
    const music = new ThemeMusic(grant, create);
    music.select(selection, false);
    music.suspend();
    expect(grant.mock.calls[0]?.[2].aborted).toBe(true);
    resolve("/audio?token=obsolete");
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
    const grant = vi.fn(async () => "/audio");
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
      async () => "/audio",
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
    const grant = vi.fn(async () => "/audio?token=short-lived");
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
    let resolve!: (url: string) => void;
    const grant = vi.fn(
      () =>
        new Promise<string>((done) => {
          resolve = done;
        }),
    );
    const create = vi.fn(audio);
    const music = new ThemeMusic(grant, create);
    music.select(selection, false);
    music.stop(true);
    resolve("/audio?token=obsolete");
    await flush();
    expect(create).not.toHaveBeenCalled();
  });

  it("recovers once from a stale playback URL and then stops retrying", async () => {
    const first = audio(),
      second = audio();
    const grant = vi.fn(async () => "/audio?token=renewed");
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
      async () => "/audio",
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
    const grant = vi.fn(async () => "/audio?token=renewed");
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
});
