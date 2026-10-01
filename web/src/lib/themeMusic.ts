export const THEME_MUSIC_INTERRUPT_EVENT = "silo:theme-music-interrupt";

export interface ThemeTrack {
  id: string;
}

export interface ThemeSelection {
  owner_id: string;
  items: ThemeTrack[];
}

/** Where to play one theme. A converted theme is a progressive stream with no
 * byte ranges, so it cannot loop by seeking back to the start. */
export interface ThemeGrant {
  url: string;
  delivery?: "original" | "converted";
}

type GrantRequest = (owner: string, theme: string, signal: AbortSignal) => Promise<ThemeGrant>;

export interface ThemeAudioFormat {
  container: string;
  audio_codec: string;
}

// Probed with canPlayType. mp4/aac is also the conversion the server offers
// when the browser cannot decode a theme's own format.
const FORMAT_PROBES: ReadonlyArray<ThemeAudioFormat & { mime: string }> = [
  { container: "mp3", audio_codec: "mp3", mime: "audio/mpeg" },
  { container: "mp4", audio_codec: "aac", mime: 'audio/mp4; codecs="mp4a.40.2"' },
  { container: "m4a", audio_codec: "aac", mime: 'audio/mp4; codecs="mp4a.40.2"' },
  { container: "m4a", audio_codec: "alac", mime: 'audio/mp4; codecs="alac"' },
  { container: "aac", audio_codec: "aac", mime: "audio/aac" },
  { container: "flac", audio_codec: "flac", mime: "audio/flac" },
  { container: "ogg", audio_codec: "vorbis", mime: 'audio/ogg; codecs="vorbis"' },
  { container: "ogg", audio_codec: "opus", mime: 'audio/ogg; codecs="opus"' },
  { container: "opus", audio_codec: "opus", mime: 'audio/ogg; codecs="opus"' },
  { container: "wav", audio_codec: "pcm", mime: 'audio/wav; codecs="1"' },
];

let cachedFormats: ThemeAudioFormat[] | undefined;

/** The theme formats this browser decodes. Empty when it cannot say, which the
 * server answers with the original audio, as before conversion existed. */
export function themeAudioFormats(
  probe: () => Pick<HTMLMediaElement, "canPlayType"> = () => document.createElement("audio"),
): ThemeAudioFormat[] {
  if (cachedFormats) return cachedFormats;
  const element = probe();
  cachedFormats = FORMAT_PROBES.filter(({ mime }) => element.canPlayType(mime) !== "").map(
    ({ container, audio_codec }) => ({ container, audio_codec }),
  );
  return cachedFormats;
}

/** Test seam: forget the probed formats. */
export function resetThemeAudioFormats() {
  cachedFormats = undefined;
}

/** Owns one detail-page audio element. Signed URLs live only in the element. */
export class ThemeMusic {
  private audio: HTMLAudioElement | null = null;
  private request: AbortController | null = null;
  private generation = 0;
  private owner = "";
  private theme = "";
  private loop = false;
  private converted = false;
  private retry = 0;
  private suspended = false;
  private fade: ReturnType<typeof setInterval> | null = null;
  private gesture: (() => void) | null = null;

  constructor(
    private readonly grant: GrantRequest,
    private readonly createAudio: () => HTMLAudioElement = () => new Audio(),
  ) {}

  select(selection: ThemeSelection | undefined, loop: boolean) {
    this.loop = loop;
    if (this.audio) this.audio.loop = loop && !this.converted;
    const song = selection?.items[0];
    if (!song || !selection) {
      this.stop();
      return;
    }
    if (this.owner === selection.owner_id && this.theme === song.id) {
      if (this.suspended) {
        this.suspended = false;
        if (this.audio) void this.play(this.audio, this.generation);
        else void this.load();
      }
      return;
    }
    this.stop();
    this.owner = selection.owner_id;
    this.theme = song.id;
    this.retry = 0;
    void this.load();
  }

  private async load() {
    const generation = ++this.generation;
    this.request?.abort();
    this.request = new AbortController();
    try {
      const grant = await this.grant(this.owner, this.theme, this.request.signal);
      if (generation !== this.generation) return;
      this.clearAudio();
      const audio = this.createAudio();
      this.audio = audio;
      this.converted = grant.delivery === "converted";
      audio.preload = "none";
      audio.volume = 0;
      audio.loop = this.loop && !this.converted;
      audio.src = grant.url;
      audio.ontimeupdate = () => {
        if (generation === this.generation && audio.currentTime > 0) this.retry = 0;
      };
      audio.onended = () => {
        // A converted stream cannot seek back, and its short-lived URL may have
        // expired, so a loop replays it with a fresh grant. A stream that ended
        // without playing counts against the same single retry as an error, so
        // a conversion that fails cleanly cannot replay forever.
        if (generation !== this.generation || !this.converted || !this.loop || this.suspended)
          return;
        if (audio.currentTime > 0 || this.retry++ === 0) void this.load();
      };
      audio.onerror = () => {
        if (generation !== this.generation) return;
        this.clearAudio();
        if (!this.suspended && this.retry++ === 0) void this.load();
      };
      await this.play(audio, generation);
    } catch {
      // Themes are optional. A refused or stale grant never disrupts the page.
    }
  }

  private async play(audio: HTMLAudioElement, generation: number) {
    if (generation !== this.generation || this.suspended) return;
    try {
      await audio.play();
      if (generation !== this.generation || this.suspended) {
        audio.pause();
        return;
      }
      this.clearGesture();
      this.fadeVolume(audio, 0.35);
    } catch (error) {
      if (generation !== this.generation || this.suspended) return;
      if (error instanceof DOMException && error.name === "NotAllowedError") {
        this.clearGesture();
        this.gesture = () => {
          this.clearGesture();
          void this.play(audio, generation);
        };
        document.addEventListener("pointerdown", this.gesture, { once: true });
        document.addEventListener("keydown", this.gesture, { once: true });
      }
    }
  }

  private clearGesture() {
    if (this.gesture) {
      document.removeEventListener("pointerdown", this.gesture);
      document.removeEventListener("keydown", this.gesture);
      this.gesture = null;
    }
  }

  private fadeVolume(audio: HTMLAudioElement, target: number, done?: () => void) {
    if (this.fade) clearInterval(this.fade);
    const start = audio.volume;
    let step = 0;
    this.fade = setInterval(() => {
      audio.volume = Math.max(0, Math.min(1, start + (target - start) * (++step / 12)));
      if (step === 12) {
        if (this.fade) clearInterval(this.fade);
        this.fade = null;
        done?.();
      }
    }, 25);
  }

  private clearAudio() {
    if (this.fade) clearInterval(this.fade);
    this.fade = null;
    this.clearGesture();
    if (this.audio) {
      this.audio.onerror = null;
      this.audio.ontimeupdate = null;
      this.audio.onended = null;
      this.audio.pause();
      this.audio.removeAttribute("src");
      this.audio.load();
      this.audio = null;
    }
  }

  suspend() {
    this.suspended = true;
    if (this.audio) {
      this.clearGesture();
      this.fadeVolume(this.audio, 0, () => this.audio?.pause());
    } else {
      this.stop(true);
    }
  }

  stop(immediate = false) {
    this.suspended = false;
    ++this.generation;
    this.request?.abort();
    this.request = null;
    this.owner = "";
    this.theme = "";
    this.clearGesture();
    if (!immediate && this.audio && !this.audio.paused) {
      this.audio.onerror = null;
      this.audio.ontimeupdate = null;
      this.audio.onended = null;
      this.fadeVolume(this.audio, 0, () => this.clearAudio());
    } else {
      this.clearAudio();
    }
  }
}
