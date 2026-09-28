type WebkitVideoElement = HTMLVideoElement & {
  webkitSupportsFullscreen?: boolean;
  webkitDisplayingFullscreen?: boolean;
  webkitEnterFullscreen?: () => void;
  webkitExitFullscreen?: () => void;
};

/** True while the document or the video element (iOS WebKit) is fullscreen. */
export function isPlayerFullscreen(video: HTMLVideoElement | null): boolean {
  return (
    !!document.fullscreenElement ||
    !!(video as WebkitVideoElement | null)?.webkitDisplayingFullscreen
  );
}

/**
 * Leaves fullscreen when anything is fullscreen, otherwise makes target
 * fullscreen. WebKit on iPhone rejects element fullscreen, so the video's own
 * native fullscreen is the fallback there.
 */
export function toggleFullscreen(target: HTMLElement | null, video: HTMLVideoElement | null) {
  const webkitVideo = video as WebkitVideoElement | null;
  const enterVideoFullscreen = () => {
    if (
      webkitVideo?.webkitSupportsFullscreen !== false &&
      typeof webkitVideo?.webkitEnterFullscreen === "function"
    ) {
      webkitVideo.webkitEnterFullscreen();
    }
  };

  if (document.fullscreenElement) {
    document.exitFullscreen().catch(() => {});
  } else if (webkitVideo?.webkitDisplayingFullscreen) {
    webkitVideo.webkitExitFullscreen?.();
  } else if (target?.requestFullscreen) {
    target.requestFullscreen().catch(enterVideoFullscreen);
  } else {
    enterVideoFullscreen();
  }
}
