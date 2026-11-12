import { useState } from "react";

import type { ScanRun } from "@/api/types";

// How many scan rows a group shows before the rest collapse behind an expander.
const COLLAPSED_SCAN_ROW_LIMIT = 4;

export function useCollapsedScans(scans: ScanRun[]) {
  const [expanded, setExpanded] = useState(false);
  const visibleScans = expanded ? scans : scans.slice(0, COLLAPSED_SCAN_ROW_LIMIT);
  return {
    expanded,
    setExpanded,
    visibleScans,
    hiddenCount: scans.length - visibleScans.length,
    collapsible: scans.length > COLLAPSED_SCAN_ROW_LIMIT,
  };
}
