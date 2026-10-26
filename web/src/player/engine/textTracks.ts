/**
 * A programmatic TextTrack driven by the engine's clock.
 *
 * The browser activates cues on a media element only as its own media
 * timeline advances, and an engine-driven element has none. The player's text
 * subtitles (useSubtitleTracks) create a track with `addTextTrack`, add VTTCues
 * and listen for `cuechange`; this stand-in offers the same surface and
 * recomputes the active cues on every engine tick.
 */
export class EngineTextTrack extends EventTarget {
  mode: TextTrackMode = "disabled";
  private cueList: TextTrackCue[] = [];
  private active: TextTrackCue[] = [];

  constructor(
    readonly kind: TextTrackKind,
    readonly label: string,
    readonly language: string,
  ) {
    super();
  }

  get cues(): TextTrackCueList | null {
    return this.mode === "disabled" ? null : (this.cueList as unknown as TextTrackCueList);
  }

  get activeCues(): TextTrackCueList | null {
    return this.mode === "disabled" ? null : (this.active as unknown as TextTrackCueList);
  }

  addCue(cue: TextTrackCue): void {
    this.cueList.push(cue);
  }

  removeCue(cue: TextTrackCue): void {
    const index = this.cueList.indexOf(cue);
    if (index >= 0) this.cueList.splice(index, 1);
  }

  /** Recomputes the active cues for `time` and fires `cuechange` when they change. */
  update(time: number): void {
    const next =
      this.mode === "disabled"
        ? []
        : this.cueList.filter((cue) => cue.startTime <= time && time < cue.endTime);
    if (next.length === this.active.length && next.every((cue, i) => cue === this.active[i])) {
      return;
    }
    this.active = next;
    this.dispatchEvent(new Event("cuechange"));
  }
}
