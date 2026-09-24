import { useCallback, useState } from "react";

// A revisioned artwork filename, `<variant>.<revision>.<ext>` (see
// internal/artworkkey). The revision is a SHA-256 over the encoded variants,
// so the object at that key never changes.
const REVISIONED_ARTWORK_FILE = /^(?:original|w\d+)\.[0-9a-f]{64}\.[^./]+$/;

/**
 * Returns the part of an image URL that identifies its bytes.
 *
 * The query of a revisioned artwork URL only authorizes the read: the local
 * route's exp/sig, an S3 presigned signature, or a CDN token. The local route
 * signs the key again when its 15-minute window rolls over; S3 and CDN modes
 * sign on every resolve, so API nodes can return different signatures for the
 * same key. For such an image the URL without its query is the identity. Any
 * other URL may carry new bytes behind the same path, so the whole URL
 * identifies it.
 */
export function imageIdentity(url: string): string {
  const end = url.search(/[?#]/);
  if (end < 0) return url;
  const path = url.slice(0, end);
  return REVISIONED_ARTWORK_FILE.test(path.slice(path.lastIndexOf("/") + 1)) ? path : url;
}

/**
 * Tracks whether the image at `url` has finished loading, keyed by the
 * image's identity (see imageIdentity).
 *
 * Artwork URLs change in place when a new immutable revision is published; a
 * plain boolean load flag would keep showing the previous revision's pixels at
 * full opacity while the replacement is still fetching. Keying the loaded
 * state by identity makes a new revision start hidden until its own load
 * event, while a URL that was only signed again stays visible: the browser
 * keeps painting the loaded pixels until the re-signed URL loads.
 *
 * Wire onError too. If the re-signed request fails, the element drops the
 * pixels it kept and shows the broken-image state; onError hides it again so
 * the placeholder shows, as it does when a first load fails.
 */
export function useImageLoaded(url: string | undefined | null): {
  loaded: boolean;
  onLoad: () => void;
  onError: () => void;
} {
  const identity = url ? imageIdentity(url) : "";
  const [loadedIdentity, setLoadedIdentity] = useState("");
  const onLoad = useCallback(() => setLoadedIdentity(identity), [identity]);
  const onError = useCallback(() => setLoadedIdentity(""), []);
  return { loaded: !!identity && loadedIdentity === identity, onLoad, onError };
}
