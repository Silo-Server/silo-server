import type { components } from "./schema";
import { v2 } from "./request";

export type WatchTogetherCapabilities = components["schemas"]["WatchTogetherCapabilities"];

/** Which room behaviors this server supports. Read once and cached by callers. */
export async function readWatchTogetherCapabilities(): Promise<WatchTogetherCapabilities> {
  return v2("GET /api/v2/watch-together/capabilities", { retryAuthentication: false });
}
