import { useEffect, useState } from "react";
import { probeEngineCapabilities, type EngineCapabilities } from "../engine/capabilities";
import { isBrowserDecodingEnabled } from "../engine/settings";

export interface BrowserEngineState {
  /** False until the probe has answered; playback start waits for it. */
  settled: boolean;
  /** Null when browser decoding is off or this browser cannot run it. */
  capabilities: EngineCapabilities | null;
}

/**
 * The browser decode engine's capabilities for this page, when the viewer has
 * turned browser decoding on. The setting is read once per player mount, so a
 * change applies from the next playback.
 */
export function useBrowserEngineCapabilities(): BrowserEngineState {
  const [state, setState] = useState<BrowserEngineState>(() => ({
    settled: !isBrowserDecodingEnabled(),
    capabilities: null,
  }));

  useEffect(() => {
    if (state.settled) return;
    let cancelled = false;
    void probeEngineCapabilities().then((capabilities) => {
      if (!cancelled) setState({ settled: true, capabilities });
    });
    return () => {
      cancelled = true;
    };
    // The decision to probe is made once, at mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return state;
}
