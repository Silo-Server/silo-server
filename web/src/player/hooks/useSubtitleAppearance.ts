import { useMemo } from "react";
import { useSubtitleAppearanceSetting } from "@/hooks/queries/subtitleAppearance";
import { computeSubtitleStyles } from "@/lib/subtitleAppearance";
import type { SubtitleAppearance, SubtitleStyles } from "@/lib/subtitleAppearance";

export function useSubtitleAppearance(): SubtitleStyles & { settings: SubtitleAppearance } {
  const { appearance } = useSubtitleAppearanceSetting();
  const styles = useMemo(() => computeSubtitleStyles(appearance), [appearance]);

  return { settings: appearance, ...styles };
}
