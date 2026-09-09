import { useLayoutEffect, useRef } from "react";

/** Keep mobile titles readable; fit desktop titles into a bounded line budget. */
export default function DetailTitle({
  title,
  className,
  bounded,
}: {
  title: string;
  className: string;
  bounded: boolean;
}) {
  const titleRef = useRef<HTMLHeadingElement>(null);

  useLayoutEffect(() => {
    const heading = titleRef.current;
    if (!bounded || !heading) return;
    const measure = document.createElement("span");
    measure.setAttribute("aria-hidden", "true");
    Object.assign(measure.style, {
      position: "fixed",
      visibility: "hidden",
      pointerEvents: "none",
      display: "block",
      overflowWrap: "anywhere",
    });
    measure.textContent = title;
    document.body.append(measure);
    let frame = 0;
    let disposed = false;
    let observedWidth = -1;

    const fit = () => {
      if (disposed) return;
      heading.style.removeProperty("font-size");
      heading.style.removeProperty("letter-spacing");
      const style = getComputedStyle(heading);
      const baseSize = parseFloat(style.fontSize);
      const baseSpacing = parseFloat(style.letterSpacing) || 0;
      const width = heading.clientWidth;
      if (!width || !Number.isFinite(baseSize)) return;
      Object.assign(measure.style, {
        width: `${width}px`,
        fontFamily: style.fontFamily,
        fontWeight: style.fontWeight,
        fontStyle: style.fontStyle,
      });

      // Descenders need a real line box, not the old 0.98 display leading.
      // The smallest step adds a third line without increasing the budget.
      const steps: ReadonlyArray<readonly [number, number]> =
        window.innerWidth < 1024
          ? [[1, 3]]
          : [
              [1, 2],
              [5 / 6, 2],
              [2 / 3, 3],
            ];
      for (const [scale, lines] of steps) {
        const size = baseSize * scale;
        measure.style.fontSize = `${size}px`;
        measure.style.letterSpacing = `${baseSpacing * scale}px`;
        measure.style.lineHeight = "1.15";
        heading.style.fontSize = `${size}px`;
        heading.style.letterSpacing = `${baseSpacing * scale}px`;
        heading.style.setProperty("--detail-title-lines", String(lines));
        if (measure.getBoundingClientRect().height <= size * 1.15 * lines + 1) break;
      }
    };
    const scheduleFit = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(fit);
    };
    const observer = new ResizeObserver(([entry]) => {
      if (!entry || entry.contentRect.width === observedWidth) return;
      observedWidth = entry.contentRect.width;
      scheduleFit();
    });
    observer.observe(heading);
    // Theme preferences can change the rem-based font without changing width.
    const typographyObserver = new MutationObserver(scheduleFit);
    typographyObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-text-scale", "data-text-weight"],
    });
    window.addEventListener("resize", scheduleFit);
    document.fonts?.addEventListener("loadingdone", scheduleFit);
    void document.fonts?.ready.then(() => {
      if (!disposed) scheduleFit();
    });
    fit();
    return () => {
      disposed = true;
      cancelAnimationFrame(frame);
      observer.disconnect();
      typographyObserver.disconnect();
      window.removeEventListener("resize", scheduleFit);
      document.fonts?.removeEventListener("loadingdone", scheduleFit);
      measure.remove();
    };
  }, [bounded, title, className]);

  return (
    <h1 ref={titleRef} title={bounded ? title : undefined} className={className}>
      {title}
    </h1>
  );
}
