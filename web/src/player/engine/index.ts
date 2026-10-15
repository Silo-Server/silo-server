/**
 * Entry point of the browser decode engine chunk. The player imports this
 * lazily, only for an `original_http` plan the browser's own player cannot
 * take, so none of the engine (Mediabunny, the worker, WebAssembly) loads for
 * ordinary playback.
 */
import type { FailureV3 } from "../protocol-v3";
import { EnginePlayer } from "./EnginePlayer";
import { attachEngineToElement } from "./facade";
import type { EngineSourceSpec } from "./source";

export interface BrowserEngineSession {
  destroy(): void;
}

/**
 * Plays `source` through `video`: the element keeps its place in the page and
 * every listener, while the picture is drawn on a canvas the engine keeps
 * inside `surface` (a box laid out under the element).
 */
export function attachBrowserEngine(options: {
  video: HTMLVideoElement;
  surface: HTMLElement;
  source: EngineSourceSpec;
  onFailure: (failure: FailureV3) => void;
}): BrowserEngineSession {
  const { video, surface, source, onFailure } = options;
  const player = new EnginePlayer({
    source,
    surface,
    element: video,
    volume: video.volume,
    muted: video.muted,
    onFailure,
  });
  const detach = attachEngineToElement(video, player);
  try {
    player.load();
  } catch (error) {
    // Never leave the overrides on the element the fallback plan will use.
    detach();
    player.destroy();
    throw error;
  }
  return {
    destroy() {
      detach();
      player.destroy();
    },
  };
}
