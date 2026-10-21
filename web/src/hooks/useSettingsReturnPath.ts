import { useEffect } from "react";
import { useLocation } from "react-router";

// Module state rather than React state: the app shell is mounted per route
// branch, so entering settings mounts a fresh shell that has to read what the
// previous one saw.
let returnPath: string | undefined;

/**
 * Where the settings sidebar's back link goes: the last page the app shell
 * showed outside settings, so leaving settings lands back on the title it was
 * opened from. Falls back to `fallback` when settings was the first page
 * loaded.
 */
export function useSettingsReturnPath(inSettings: boolean, fallback: string): string {
  const location = useLocation();
  const here = `${location.pathname}${location.search}`;

  useEffect(() => {
    if (!inSettings) returnPath = here;
  }, [here, inSettings]);

  return returnPath ?? fallback;
}
