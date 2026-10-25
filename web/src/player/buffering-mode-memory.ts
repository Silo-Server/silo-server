import { storage } from "@/utils/storage";
import type { BufferingMode } from "./buffering-quality";

/**
 * The last "Lower quality when buffering" mode this browser saw for each
 * profile. A start does not wait for the settings read, so until it answers
 * the player uses the mode it saw last, and the default only when it has seen
 * none: a `never` profile must not lose its `ignore` on a cold start.
 */
const MODES: readonly BufferingMode[] = ["never", "ask", "always"];

function read(): Record<string, BufferingMode> {
  try {
    const parsed = JSON.parse(storage.get(storage.KEYS.BUFFERING_MODES) ?? "{}") as unknown;
    if (!parsed || typeof parsed !== "object") return {};
    return Object.fromEntries(
      Object.entries(parsed).filter(([, mode]) => MODES.includes(mode as BufferingMode)),
    ) as Record<string, BufferingMode>;
  } catch {
    return {};
  }
}

export function lastBufferingMode(profileId: string | null): BufferingMode | undefined {
  return profileId ? read()[profileId] : undefined;
}

export function rememberBufferingMode(profileId: string | null, mode: BufferingMode): void {
  if (!profileId) return;
  const modes = read();
  if (modes[profileId] === mode) return;
  storage.set(storage.KEYS.BUFFERING_MODES, JSON.stringify({ ...modes, [profileId]: mode }));
}
