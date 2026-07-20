export const HLS_STARTUP_TIMEOUT_MS = 60_000;

const MAX_FATAL_NETWORK_RECOVERIES = 1;

type StartupState = "starting" | "playable" | "failed" | "disposed";

export class HlsStartupGuard {
  private state: StartupState = "starting";
  private fatalNetworkRecoveries = 0;
  private timeoutId: ReturnType<typeof setTimeout> | null;

  constructor(private readonly onFailure: () => void) {
    this.timeoutId = setTimeout(() => this.fail(), HLS_STARTUP_TIMEOUT_MS);
  }

  handleFatalNetworkError(): boolean {
    if (this.state === "playable") return true;
    if (this.state !== "starting") return false;

    if (this.fatalNetworkRecoveries < MAX_FATAL_NETWORK_RECOVERIES) {
      this.fatalNetworkRecoveries++;
      return true;
    }

    this.fail();
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

  private fail() {
    if (this.state !== "starting") return;

    this.state = "failed";
    this.clearTimeout();
    this.onFailure();
  }

  private clearTimeout() {
    if (this.timeoutId === null) return;

    clearTimeout(this.timeoutId);
    this.timeoutId = null;
  }
}
