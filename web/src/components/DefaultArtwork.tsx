import { cn } from "@/lib/utils";

/**
 * What a poster, cover or still shows when the title has no artwork: the
 * Silo brand colours as a soft glow, the same for every title. The Apple and
 * Android clients draw the same glow, so keep the stops in `.default-artwork`
 * (app.css) in step with them. Fills its positioned parent.
 */
export default function DefaultArtwork({ className }: { className?: string }) {
  return <div aria-hidden="true" className={cn("default-artwork absolute inset-0", className)} />;
}
