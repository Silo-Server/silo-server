import { useContext } from "react";

import { UICustomizationContext } from "@/contexts/uiCustomizationContext";

export function useUICustomization() {
  return useContext(UICustomizationContext);
}
