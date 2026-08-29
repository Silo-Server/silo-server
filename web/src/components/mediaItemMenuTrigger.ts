import { cn } from "@/lib/utils";

export type PosterActionDensity = "standard" | "compact" | "narrow";

type PosterActionSizeClasses = {
  icon: string;
  trigger: string;
};

const POSTER_ACTION_SIZE_CLASSES: Record<PosterActionDensity | "wide", PosterActionSizeClasses> = {
  wide: { icon: "size-5", trigger: "size-9" },
  narrow: { icon: "size-3", trigger: "size-6" },
  compact: { icon: "size-3 sm:size-3.5", trigger: "size-6 sm:size-7" },
  standard: { icon: "size-3 sm:size-4", trigger: "size-6 sm:size-8" },
};

function posterActionSizeClasses(variant: "poster" | "wide", density: PosterActionDensity) {
  return POSTER_ACTION_SIZE_CLASSES[variant === "wide" ? "wide" : density];
}

export function mediaItemMenuIconClassName(
  variant: "poster" | "wide" = "poster",
  density: PosterActionDensity = "standard",
) {
  return posterActionSizeClasses(variant, density).icon;
}

export function mediaItemMenuTriggerClassName(
  variant: "poster" | "wide" = "poster",
  density: PosterActionDensity = "standard",
) {
  return cn(
    "media-card-action-trigger inline-flex items-center justify-center rounded-md border border-border/20 bg-background/80 text-foreground shadow-sm transition-[opacity,background-color,color] duration-150 hover:bg-background/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/70",
    posterActionSizeClasses(variant, density).trigger,
  );
}
