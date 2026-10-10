import { cn } from "@/lib/utils";

/** The Featured hero's overview paragraph. */
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
        "text-foreground/72 line-clamp-2 text-sm leading-7 sm:line-clamp-none sm:text-base",
        translating && "animate-pulse opacity-50",
      )}
    >
      {overview}
    </p>
  );
}
