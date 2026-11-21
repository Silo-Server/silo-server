import { useClampCut } from "@/hooks/useClampCut";

/**
 * The Featured hero's overview paragraph. The hero anchors its content to the
 * bottom and grows only when the content does not fit its minimum height, so
 * the overview stays clamped at every breakpoint: an unbounded synopsis would
 * make the hero, and the page rows below it, jump in height between slides.
 */
export function HeroOverviewText({ overview }: { overview: string }) {
  const ref = useClampCut<HTMLParagraphElement>(overview);
  return (
    <p
      ref={ref}
      dir="auto"
      className="bidi-text text-foreground/72 line-clamp-2 text-sm leading-7 sm:line-clamp-3 sm:text-base"
    >
      {overview}
    </p>
  );
}
