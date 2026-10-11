// Canonical media-spec formatting helpers shared by item detail views, the
// player stats overlay, poster overlays, and admin views. Display policy
// differences between surfaces (empty-string vs "—" fallback, GB vs GiB
// labels) are expressed via parameters here instead of per-surface copies.

export const CODEC_LABELS: Record<string, string> = {
  aac: "AAC",
  ac3: "AC3",
  av1: "AV1",
  dts: "DTS",
  dtshd: "DTS-HD",
  eac3: "EAC3",
  flac: "FLAC",
  h264: "H.264",
  hevc: "HEVC",
  mp3: "MP3",
  opus: "Opus",
  truehd: "TrueHD",
};

/** Exact-match codec display label; unknown codecs are uppercased. */
export function formatCodecLabel(codec?: string, fallback = "—"): string {
  const normalized = codec?.trim();
  if (!normalized) {
    return fallback;
  }
  return CODEC_LABELS[normalized.toLowerCase()] ?? normalized.toUpperCase();
}

const ATMOS_WORD = /\batmos\b/i;

// Codec families that cannot carry Atmos. A compatibility track of one of
// them is often titled after its source ("AC3 5.1 (from TrueHD Atmos)"), so
// its titles are not evidence. E-AC-3 contains "ac-3" and is checked first.
const NON_ATMOS_CODECS = [
  "aac",
  "ac3",
  "ac-3",
  "dts",
  "flac",
  "mp3",
  "opus",
  "vorbis",
  "pcm",
  "alac",
];

function codecCanCarryAtmos(lowerCodec: string): boolean {
  if (
    lowerCodec.includes("truehd") ||
    lowerCodec.includes("eac3") ||
    lowerCodec.includes("e-ac-3")
  ) {
    return true;
  }
  return !NON_ATMOS_CODECS.some((family) => lowerCodec.includes(family));
}

/** Whether text names Atmos as a word ("Atmospheric" does not). */
export function mentionsAtmos(text?: string): boolean {
  return Boolean(text && ATMOS_WORD.test(text));
}

function namesAtmos(lowerCodec: string, atmosEvidence?: string): boolean {
  if (lowerCodec.includes("atmos")) return true;
  return mentionsAtmos(atmosEvidence) && codecCanCarryAtmos(lowerCodec);
}

/**
 * Fuzzy audio-family label for quality badges. The stored codec of an Atmos
 * track is plain "eac3" or "truehd", so atmosEvidence carries any other text
 * that can name Atmos, such as the track's FFprobe profile ("Dolby Digital
 * Plus + Dolby Atmos"). It counts only for a codec that can carry Atmos or
 * an unknown one. audioTrackLabel builds it from a track.
 */
export function mapAudioLabel(codec: string, atmosEvidence?: string): string {
  const lower = codec.toLowerCase();
  if (namesAtmos(lower, atmosEvidence)) {
    if (lower.includes("truehd")) return "TrueHD Atmos";
    if (lower.includes("eac3") || lower.includes("e-ac-3")) return "DD+ Atmos";
    return "Atmos";
  }
  if (lower.includes("truehd")) return "TrueHD";
  if (lower.includes("dts-hd") || lower.includes("dts:x")) return "DTS-HD";
  if (lower.includes("dts")) return "DTS";
  if (lower.includes("eac3") || lower.includes("e-ac-3")) return "EAC3";
  if (lower.includes("aac")) return "AAC";
  if (lower.includes("flac")) return "FLAC";
  return codec.toUpperCase();
}

/** The audio track fields that can identify a codec and Atmos. */
export interface AudioTrackFormat {
  codec?: string;
  profile?: string;
  title?: string;
  embedded_title?: string;
}

/**
 * Text beyond the codec that can name Atmos: the FFprobe profile, and the
 * track titles, which some releases tag "Atmos" when the profile is empty.
 */
export function audioTrackAtmosEvidence(track: AudioTrackFormat): string {
  return [track.profile, track.title, track.embedded_title].filter(Boolean).join(" ");
}

/** Whether a track's codec, profile, or titles name Atmos, as audioTrackLabel reads them. */
export function audioTrackHasAtmos(track: AudioTrackFormat): boolean {
  const codec = track.codec?.toLowerCase();
  return codec ? namesAtmos(codec, audioTrackAtmosEvidence(track)) : false;
}

/** Audio-family label for one track ("DD+ Atmos", "TrueHD", …), or "" without a codec. */
export function audioTrackLabel(track: AudioTrackFormat): string {
  return track.codec ? mapAudioLabel(track.codec, audioTrackAtmosEvidence(track)) : "";
}

interface FormatFileSizeOptions {
  fallback?: string;
  /** Use IEC unit labels (GiB/MiB/KiB) instead of GB/MB/KB. Math is 1024-based either way. */
  iecUnits?: boolean;
}

export function formatFileSize(bytes?: number, options: FormatFileSizeOptions = {}): string {
  const { fallback = "", iecUnits = false } = options;
  if (!isPositive(bytes)) return fallback;
  const [giga, mega, kilo] = iecUnits ? ["GiB", "MiB", "KiB"] : ["GB", "MB", "KB"];
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} ${giga}`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} ${mega}`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} ${kilo}`;
  return `${bytes} B`;
}

export function formatBitrate(kbps?: number, fallback = ""): string {
  if (!isPositive(kbps)) return fallback;
  return `${Math.round(kbps).toLocaleString()} kbps`;
}

export function formatMbpsFromKbps(kbps?: number, fallback = "—"): string {
  if (!isPositive(kbps)) return fallback;
  return `${(kbps / 1000).toFixed(1)} Mbps`;
}

export function formatSampleRate(sampleRate?: number, fallback = ""): string {
  if (!isPositive(sampleRate)) return fallback;
  return `${sampleRate.toLocaleString()} Hz`;
}

export function formatChannels(channels?: number): string {
  if (!channels || channels <= 0) return "";
  if (channels === 8) return "7.1";
  if (channels === 6) return "5.1";
  if (channels === 2) return "stereo";
  return `${channels} ch`;
}

/** Ensures a raw Dolby Vision label ("Profile 8.1") carries the "Dolby Vision" prefix. */
export function dolbyVisionLabel(raw: string): string {
  return raw.toLowerCase().startsWith("dolby vision") ? raw : `Dolby Vision ${raw}`;
}

// Kometa-style pretty resolution label: "2160p" → "4K", others keep their
// lowercase-p form ("1080p", "720p"). Unknown values are uppercased so they
// at least look intentional.
export function prettyResolution(value: string | undefined): string | null {
  if (!value) return null;
  const v = value.toLowerCase().trim();
  if (v === "") return null;
  if (v === "2160p" || v === "4k" || v === "uhd") return "4K";
  if (v === "4320p" || v === "8k") return "8K";
  if (/^\d+p$/.test(v)) return v;
  return value.toUpperCase();
}

// Compact HDR suffix used for combined Resolution+HDR badges. Any Dolby
// Vision variant collapses to "DV"; any other HDR variant to "HDR".
export function compactHdrSuffix(value: string | undefined): string | null {
  if (!value) return null;
  if (value.includes("DV")) return "DV";
  return "HDR";
}

function isPositive(value?: number): value is number {
  return Number.isFinite(value) && value != null && value > 0;
}

/** Hero runtime badge label ("2h 43m", "43m"); "" for zero/unknown. */
export function formatRuntimeMinutes(minutes: number): string {
  if (minutes <= 0) return "";
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}
