import { cn } from "@/lib/utils";
import { useBranding } from "@/hooks/useBranding";

const SILO_WORDMARK_SRC = "/silo-wordmark-sidebar.png";
const SILO_MARK_SRC = "/silo-icon-1024.png";

export type SiloBrandVariant = "wordmark" | "mark";

interface SiloBrandProps {
  className?: string;
  imageClassName?: string;
  variant?: SiloBrandVariant;
}

export function SiloBrand({ className, imageClassName, variant = "wordmark" }: SiloBrandProps) {
  const isMark = variant === "mark";
  const { serverName, wordmarkUrl, markUrl } = useBranding();

  const customSrc = isMark ? markUrl : wordmarkUrl;
  const src = customSrc ?? (isMark ? SILO_MARK_SRC : SILO_WORDMARK_SRC);

  return (
    <span className={cn("block shrink-0", !isMark && "overflow-hidden", className)}>
      <img
        src={src}
        alt={serverName}
        className={cn("h-full w-full object-contain", isMark && "rounded-lg", imageClassName)}
      />
    </span>
  );
}
