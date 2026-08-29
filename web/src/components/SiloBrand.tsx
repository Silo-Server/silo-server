import { cn } from "@/lib/utils";
import { useBranding } from "@/hooks/useBranding";
import { useOptionalTheme } from "@/hooks/useTheme";
import { THEMES } from "@/lib/themes";

/** White-text wordmark, for dark surfaces. */
const SILO_WORDMARK_SRC = "/silo-wordmark-sidebar.png";
/** Dark-text wordmark, for light surfaces. */
const SILO_WORDMARK_LIGHT_SRC = "/silo-wordmark-sidebar-light.png";
/** The square mark reads on both appearances, so it has no variant. */
const SILO_MARK_SRC = "/silo-icon-1024.png";

export type SiloBrandVariant = "wordmark" | "mark";

interface SiloBrandProps {
  className?: string;
  imageClassName?: string;
  variant?: SiloBrandVariant;
}

export function SiloBrand({ className, imageClassName, variant = "wordmark" }: SiloBrandProps) {
  const isMark = variant === "mark";
  const { serverName, wordmarkUrl, markUrl, wordmarkLightUrl, markLightUrl } = useBranding();
  // This renders outside ThemeProvider too (login chrome, tests), where the
  // dark appearance is the right default.
  const theme = useOptionalTheme();
  const appearance = theme ? THEMES[theme.activeTheme].appearance : "dark";
  const isLight = appearance === "light";

  // On light surfaces the light variant wins, then the main custom asset (an
  // admin who uploaded only one presumably wants it everywhere), then the
  // bundled default for the appearance.
  const src = isMark
    ? ((isLight ? (markLightUrl ?? markUrl) : markUrl) ?? SILO_MARK_SRC)
    : isLight
      ? (wordmarkLightUrl ?? wordmarkUrl ?? SILO_WORDMARK_LIGHT_SRC)
      : (wordmarkUrl ?? SILO_WORDMARK_SRC);

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
