// The server keeps a plan's session for at least 90s so the next fallback can
// still be requested (docs/architecture/playback-protocol-v3.md §2.5).
export const STARTUP_TIMEOUT_MS = 60_000;

const MAX_FATAL_NETWORK_RECOVERIES = 1;

type StartupState = "starting" | "playable" | "failed" | "disposed";

export class StartupGuard {
  private state: StartupState = "starting";
  private fatalNetworkRecoveries = 0;
  private timeoutId: ReturnType<typeof setTimeout> | null;

  /** `onFailure` hears whether the deadline passed or fatal network errors ran out. */
  constructor(private readonly onFailure: (cause: "deadline" | "network") => void) {
    this.timeoutId = setTimeout(() => this.fail("deadline"), STARTUP_TIMEOUT_MS);
  }

  handleFatalNetworkError(): boolean {
    if (this.state === "playable") return true;
    if (this.state !== "starting") return false;

    if (this.fatalNetworkRecoveries < MAX_FATAL_NETWORK_RECOVERIES) {
      this.fatalNetworkRecoveries++;
      return true;
    }

    this.fail("network");
    return false;
  }

  markPlaybackStarted() {
    if (this.state !== "starting") return;

    this.state = "playable";
    this.clearTimeout();
  }

  hasFailed() {
    return this.state === "failed";
  }

  dispose() {
    this.state = "disposed";
    this.clearTimeout();
  }

  private fail(cause: "deadline" | "network") {
    if (this.state !== "starting") return;

    this.state = "failed";
    this.clearTimeout();
    this.onFailure(cause);
  }

  private clearTimeout() {
    if (this.timeoutId === null) return;

    clearTimeout(this.timeoutId);
    this.timeoutId = null;
  }
}
