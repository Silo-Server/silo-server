import type { EnginePlayer } from "./EnginePlayer";

/**
 * Members of HTMLMediaElement/HTMLVideoElement that the engine takes over on
 * the player's `<video>` while it plays a file. Everything else (layout,
 * listeners, fullscreen, the element's identity in React refs) stays the real
 * element's, so the player's hooks keep working unchanged.
 */
const DELEGATED_PROPERTIES = [
  "currentTime",
  "duration",
  "paused",
  "ended",
  "seeking",
  "readyState",
  "buffered",
  "seekable",
  "volume",
  "muted",
  "playbackRate",
  "videoWidth",
  "videoHeight",
] as const;

const DELEGATED_METHODS = [
  "play",
  "pause",
  "requestVideoFrameCallback",
  "cancelVideoFrameCallback",
  "addTextTrack",
] as const;

const OWN_MEMBERS = [
  ...DELEGATED_PROPERTIES,
  ...DELEGATED_METHODS,
  "error",
  "requestPictureInPicture",
  "disablePictureInPicture",
] as const;

/**
 * Installs instance-level overrides on `video` that forward to `player`, and
 * returns a function that removes them. Instance properties shadow the
 * prototype accessors, so deleting them restores the native element exactly.
 */
export function attachEngineToElement(video: HTMLVideoElement, player: EnginePlayer): () => void {
  const target = player as unknown as Record<string, unknown>;
  for (const name of DELEGATED_PROPERTIES) {
    Object.defineProperty(video, name, {
      configurable: true,
      get: () => target[name],
      set: (value: unknown) => {
        target[name] = value;
      },
    });
  }
  for (const name of DELEGATED_METHODS) {
    const method = target[name] as (...args: unknown[]) => unknown;
    Object.defineProperty(video, name, {
      configurable: true,
      writable: true,
      value: (...args: unknown[]) => method.apply(player, args),
    });
  }
  // Failures go to the player's typed failure path instead of a MediaError.
  Object.defineProperty(video, "error", { configurable: true, get: () => null });
  // The picture lives on a canvas, which the browser cannot float.
  Object.defineProperty(video, "requestPictureInPicture", {
    configurable: true,
    writable: true,
    value: () =>
      Promise.reject(
        new DOMException("Picture-in-picture is unavailable for this stream.", "NotSupportedError"),
      ),
  });
  Object.defineProperty(video, "disablePictureInPicture", { configurable: true, get: () => true });

  return () => {
    const volume = player.volume;
    const muted = player.muted;
    for (const name of OWN_MEMBERS) {
      delete (video as unknown as Record<string, unknown>)[name];
    }
    // Hand the viewer's volume back to the element the next stream will use.
    video.volume = volume;
    video.muted = muted;
  };
}
