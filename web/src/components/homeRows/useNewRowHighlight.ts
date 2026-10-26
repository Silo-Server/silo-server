import { useEffect, useState } from "react";

/** How long a newly added row stays highlighted. */
const NEW_ROW_HIGHLIGHT_MS = 2500;

/** The row just added, for a short highlight; set it after an add, and it clears itself. */
export function useNewRowHighlight() {
  const [highlightId, setHighlightId] = useState<string | null>(null);
  useEffect(() => {
    if (!highlightId) return;
    const timer = setTimeout(() => setHighlightId(null), NEW_ROW_HIGHLIGHT_MS);
    return () => clearTimeout(timer);
  }, [highlightId]);
  return [highlightId, setHighlightId] as const;
}
