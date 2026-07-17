import { useCallback, useState } from "react";

/**
 * Tracks whether the image at `url` has finished loading, keyed by URL.
 *
 * Artwork URLs change in place when a new immutable revision is published; a
 * plain boolean load flag would keep showing the previous revision's pixels at
 * full opacity while the replacement is still fetching. Keying the loaded
 * state by URL makes a changed `src` start hidden until its own load event.
 */
export function useImageLoaded(url: string | undefined | null): {
  loaded: boolean;
  onLoad: () => void;
} {
  const [loadedUrl, setLoadedUrl] = useState("");
  const onLoad = useCallback(() => setLoadedUrl(url ?? ""), [url]);
  return { loaded: !!url && loadedUrl === url, onLoad };
}
