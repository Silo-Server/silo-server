import { createContext, useContext } from "react";
import type { RefObject } from "react";

/**
 * The element the player makes fullscreen, provided by a host whose element
 * outlives the player. Without a provider the player uses its own container.
 */
export const PlayerFullscreenRootContext = createContext<RefObject<HTMLElement | null> | null>(
  null,
);

export function usePlayerFullscreenRoot(): RefObject<HTMLElement | null> | null {
  return useContext(PlayerFullscreenRootContext);
}
