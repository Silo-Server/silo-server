import { useEffect, useRef } from "react";

/** The element showing the picture: the <video>, or the decode engine's canvas. */
export type AmbientSource = HTMLVideoElement | HTMLCanvasElement;

interface AmbientLetterboxProps {
  getSource: () => AmbientSource | null;
  /** The picture's band within the player, px from the player's top edge. */
  pictureTop: number;
  pictureBottom: number;
  /** The player's height, px. */
  height: number;
  /** Redraws only while the controls show; hidden, the last frame fades out. */
  active: boolean;
}

/** Share of the picture's height each band samples from the edge it touches. */
const EDGE_SHARE = 0.3;
const REFRESH_MS = 1000;

/**
 * Lets the picture's own colour spill into the empty letterbox above and below
 * it while the HUD shows on a phone held upright, so the controls sit on a lit
 * field rather than in a black void. Each band is a tiny, blurred copy of the
 * picture strip it touches; it reads pixels only to draw them, never back.
 */
export function AmbientLetterbox({
  getSource,
  pictureTop,
  pictureBottom,
  height,
  active,
}: AmbientLetterboxProps) {
  const topRef = useRef<HTMLCanvasElement>(null);
  const bottomRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    if (!active) return;
    const draw = () => {
      const source = getSource();
      if (!source) return;
      // The picture's rows in the source's own pixels.
      let frame: { width: number; top: number; bottom: number };
      if (source instanceof HTMLVideoElement) {
        if (source.readyState < HTMLMediaElement.HAVE_CURRENT_DATA) return;
        frame = { width: source.videoWidth, top: 0, bottom: source.videoHeight };
      } else {
        // The engine's canvas spans the player and letterboxes the picture
        // itself, at the same rows as the band.
        const scale = source.height / height;
        frame = { width: source.width, top: pictureTop * scale, bottom: pictureBottom * scale };
      }
      const strip = (frame.bottom - frame.top) * EDGE_SHARE;
      if (frame.width < 1 || strip < 1) return;
      const paint = (canvas: HTMLCanvasElement | null, sourceY: number) => {
        const context = canvas?.getContext("2d");
        if (!canvas || !context) return;
        try {
          context.drawImage(
            source,
            0,
            sourceY,
            frame.width,
            strip,
            0,
            0,
            canvas.width,
            canvas.height,
          );
        } catch {
          // A source with no decoded frame yet; the next tick retries.
        }
      };
      paint(topRef.current, frame.top);
      paint(bottomRef.current, frame.bottom - strip);
    };
    draw();
    const timer = window.setInterval(draw, REFRESH_MS);
    return () => window.clearInterval(timer);
  }, [getSource, pictureTop, pictureBottom, height, active]);

  return (
    <>
      <div
        aria-hidden="true"
        className="hud-ambient hud-ambient--top"
        style={{ top: 0, height: pictureTop }}
      >
        <canvas ref={topRef} width={24} height={8} />
      </div>
      <div
        aria-hidden="true"
        className="hud-ambient hud-ambient--bottom"
        style={{ top: pictureBottom, height: Math.max(0, height - pictureBottom) }}
      >
        <canvas ref={bottomRef} width={24} height={8} />
      </div>
    </>
  );
}
