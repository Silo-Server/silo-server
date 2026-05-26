import { cn } from "@/lib/utils";

export const SUBTITLE_PROVIDER_OPTIONS = [
  { value: "all", label: "All" },
  { value: "upload", label: "Upload" },
  { value: "opensubtitles", label: "OpenSubtitles" },
  { value: "subdl", label: "SubDL" },
  { value: "subsource", label: "SubSource" },
] as const;

export function providerBadgeClass(provider: string): string {
  switch (provider) {
    case "upload":
      return "border-amber-500/35 bg-amber-500/12 text-amber-100";
    case "opensubtitles":
      return "border-sky-500/30 bg-sky-500/10 text-sky-100";
    case "subdl":
      return "border-emerald-500/30 bg-emerald-500/10 text-emerald-100";
    case "subsource":
      return "border-violet-500/30 bg-violet-500/10 text-violet-100";
    default:
      return "border-border/70 bg-muted/40 text-muted-foreground";
  }
}

export function providerLabel(provider: string): string {
  return SUBTITLE_PROVIDER_OPTIONS.find((option) => option.value === provider)?.label ?? provider;
}

export function languageChipClass(): string {
  return "border-primary/25 bg-primary/10 text-foreground";
}

export function formatChipClass(): string {
  return "border-border/60 bg-muted/30 font-mono text-[10px] uppercase tracking-[0.14em] text-muted-foreground";
}

export function staggerRowClass(index: number): string {
  const capped = Math.min(index, 8);
  return cn("motion-safe:animate-in motion-safe:fade-in motion-safe:duration-300", {
    "motion-safe:delay-0": capped === 0,
    "motion-safe:delay-[40ms]": capped === 1,
    "motion-safe:delay-[80ms]": capped === 2,
    "motion-safe:delay-[120ms]": capped === 3,
    "motion-safe:delay-[160ms]": capped === 4,
    "motion-safe:delay-[200ms]": capped === 5,
    "motion-safe:delay-[240ms]": capped === 6,
    "motion-safe:delay-[280ms]": capped === 7,
    "motion-safe:delay-[320ms]": capped >= 8,
  });
}

export function basenameFromPath(filePath: string): string {
  if (!filePath) return "—";
  const parts = filePath.split(/[/\\]/);
  return parts[parts.length - 1] || filePath;
}
