import { cn } from "@/lib/utils";

/**
 * The Featured hero's overview paragraph. The hero has a fixed height and
 * anchors its content to the bottom, so the overview stays clamped at every
 * breakpoint: an unbounded synopsis would push the title off the card.
 */
export function HeroOverviewText({
  overview,
  translating = false,
}: {
  overview: string;
  translating?: boolean;
}) {
  return (
    <p
      className={cn(
        "text-foreground/72 line-clamp-2 text-sm leading-7 sm:line-clamp-3 sm:text-base",
        translating && "animate-pulse opacity-50",
      )}
    >
      {overview}
    </p>
  );
}
