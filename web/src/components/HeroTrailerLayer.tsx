import { useEffect, type RefObject } from "react";

import { HERO_TRAILER_FADE_IN_MS, type HeroTrailerStage } from "./heroTrailer";

interface HeroTrailerLayerProps {
  /**
   * The element itself, owned by the hero rather than by this layer: the sound
   * control has to write to it inside the user's gesture, which is the only way
   * a clip that began muted can be made audible in Safari.
   */
  videoRef: RefObject<HTMLVideoElement | null>;
  clipUrl: string;
  stage: HeroTrailerStage;
  /**
   * Muted unless the viewer asked otherwise. This is not shyness: a clip with
   * sound cannot autoplay at all, so every hero clip starts silent.
   */
  muted: boolean;
  /** The sequence reached the clip and the hero is on screen in a visible tab. */
  playing: boolean;
  /** The clip can play: the fade may begin. */
  onReady: () => void;
  /** Playback position as a 0-1 fraction, for the progress rail. */
  onProgress: (fraction: number) => void;
  /** The clip played to its end; the carousel moves on. */
  onEnded: () => void;
  /** The clip failed, was blocked, or cannot play at all. */
  onUnavailable: () => void;
}

/**
 * The hero's clip: one `<video>` over the backdrop still. It is mounted with its
 * slide (so it downloads while the still is held) but stays invisible until the
 * sequence reaches `video`.
 *
 * The element keeps its audio track — the file is not silent, only the element
 * is — so the hero's sound control has something to un-mute.
 */
export default function HeroTrailerLayer({
  videoRef,
  clipUrl,
  stage,
  muted,
  playing,
  onReady,
  onProgress,
  onEnded,
  onUnavailable,
}: HeroTrailerLayerProps) {
  useEffect(() => {
    const video = videoRef.current;

    if (!video) {
      return;
    }

    // React does not reliably re-apply `muted` to a mounted element, and both
    // autoplay and the sound control depend on the two agreeing.
    video.muted = muted;

    if (playing) {
      // jsdom and a few older browsers answer `undefined` rather than a promise,
      // so the rejection is only read when there is one.
      const started = video.play();

      if (started && typeof started.catch === "function") {
        void started.catch(() => onUnavailable());
      }
    } else if (!video.paused) {
      video.pause();
    }
  }, [videoRef, muted, playing, onUnavailable]);

  /*
   * A slide that replays its own clip (the only slide, or the tour come round)
   * starts the sequence again on the same element, so the ended clip has to be
   * rewound before the still can hand the screen back to it.
   */
  useEffect(() => {
    const video = videoRef.current;

    if (video && stage !== "video" && video.ended) {
      video.currentTime = 0;
    }
  }, [videoRef, stage]);

  return (
    <video
      ref={videoRef}
      className="hero-trailer-video"
      data-visible={stage === "video" ? "true" : "false"}
      style={{ transitionDuration: `${HERO_TRAILER_FADE_IN_MS}ms` }}
      src={clipUrl}
      muted={muted}
      playsInline
      preload="auto"
      aria-hidden="true"
      onCanPlay={onReady}
      onTimeUpdate={(event) => {
        const video = event.currentTarget;
        if (video.duration > 0) {
          onProgress(Math.min(1, video.currentTime / video.duration));
        }
      }}
      onEnded={onEnded}
      onError={onUnavailable}
    />
  );
}

