import { useContext } from "react";

import { BrandingContext } from "@/contexts/BrandingProvider";
import type { BrandingContextValue } from "@/contexts/BrandingProvider";

/**
 * Returns the current server branding (name, subtitle, accent, default theme,
 * and custom asset URLs). Safe to call outside BrandingProvider — it yields
 * sensible defaults rather than throwing.
 */
export function useBranding(): BrandingContextValue {
  return useContext(BrandingContext);
}
