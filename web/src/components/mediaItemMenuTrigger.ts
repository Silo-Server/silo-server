import { cn } from "@/lib/utils";

export function mediaItemMenuTriggerClassName(variant: "poster" | "wide" = "poster") {
  return cn(
    "inline-flex items-center justify-center rounded-md border border-border/20 bg-background/60 text-foreground shadow-sm backdrop-blur-sm transition-[opacity,background-color,color] duration-150 hover:bg-background/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/70",
    variant === "wide" ? "size-9" : "size-8",
    "opacity-100 pointer-fine:opacity-0 pointer-fine:group-hover/card:opacity-100 pointer-fine:data-[state=open]:opacity-100 pointer-fine:focus-visible:opacity-100",
  );
}
