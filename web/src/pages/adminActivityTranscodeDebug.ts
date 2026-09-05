import type { AdminSession, OperationalLogEntry } from "@/api/types";

export interface TranscodeDebugFact {
  key: string;
  label: string;
  value: string;
  wide?: boolean;
}

export interface TranscodeDebugSection {
  title: string;
  facts: TranscodeDebugFact[];
}

export interface TranscodeDebugModel {
  attrs: Record<string, unknown>;
  command: string | null;
  sections: TranscodeDebugSection[];
}

interface DebugField {
  key: string;
  label: string;
  kind?:
    | "audio_track"
    | "bit_depth"
    | "boolean"
    | "bitrate"
    | "channels"
    | "duration"
    | "pipeline"
    | "pixel_formats"
    | "subtitle_track";
  wide?: boolean;
}

const DEBUG_SECTIONS: Array<{ title: string; fields: DebugField[] }> = [
  {
    title: "Execution",
    fields: [
      { key: "execution_mode", label: "Mode" },
      { key: "node_type", label: "Node type" },
      { key: "node_id", label: "Node" },
      { key: "transcode_pipeline", label: "Pipeline", kind: "pipeline" },
      { key: "hw_accel", label: "Hardware" },
      { key: "software_video_decode", label: "CPU decode", kind: "boolean" },
      { key: "hw_device", label: "GPU device" },
      { key: "video_encoder", label: "Video encoder" },
      { key: "audio_encoder", label: "Audio encoder" },
    ],
  },
  {
    title: "Video",
    fields: [
      { key: "source_video_codec", label: "Source codec" },
      { key: "source_video_profile", label: "Source profile" },
      { key: "source_video_bit_depth", label: "Source depth", kind: "bit_depth" },
      { key: "source_video_resolution", label: "Source size" },
      { key: "target_video_codec", label: "Target codec" },
      { key: "target_resolution", label: "Target size" },
      { key: "target_bitrate_kbps", label: "Video limit", kind: "bitrate" },
      { key: "video_pixel_formats", label: "Pixel formats", kind: "pixel_formats" },
      { key: "video_filter", label: "Filter graph", wide: true },
      { key: "video_bitstream_filter", label: "Bitstream filter", wide: true },
      { key: "video_sample_entry", label: "Sample entry" },
    ],
  },
  {
    title: "Audio & subtitles",
    fields: [
      { key: "source_audio_channels", label: "Source channels", kind: "channels" },
      { key: "target_audio_codec", label: "Target codec" },
      { key: "target_audio_channels", label: "Target channels", kind: "channels" },
      { key: "target_audio_bitrate_kbps", label: "Audio limit", kind: "bitrate" },
      { key: "audio_track_index", label: "Audio track", kind: "audio_track" },
      { key: "subtitle_track_index", label: "Subtitle track", kind: "subtitle_track" },
      { key: "subtitle_burn_in", label: "Burn-in", kind: "boolean" },
      { key: "subtitle_codec", label: "Subtitle codec" },
    ],
  },
  {
    title: "HDR / tone map",
    fields: [
      { key: "tone_map_policy", label: "Policy" },
      { key: "tone_map_mode", label: "Mode" },
      { key: "tone_map_source_kind", label: "Source" },
      { key: "tone_map_filter", label: "Filter", wide: true },
      { key: "tone_map_recipe_version", label: "Recipe" },
      { key: "tone_map_preflight_required", label: "Preflight", kind: "boolean" },
    ],
  },
  {
    title: "HLS & cache",
    fields: [
      { key: "segment_type", label: "Segment type" },
      { key: "segment_duration_seconds", label: "Segment length", kind: "duration" },
      { key: "segment_retention_seconds", label: "Back cache", kind: "duration" },
      { key: "throttle_configured", label: "Throttle known", kind: "boolean" },
      { key: "throttle_enabled", label: "Throttle", kind: "boolean" },
      { key: "throttle_threshold_seconds", label: "Forward buffer", kind: "duration" },
      { key: "throttle_paused", label: "FFmpeg paused", kind: "boolean" },
      { key: "throttle_gap_seconds", label: "Current lead", kind: "duration" },
      { key: "fast_start", label: "Fast start", kind: "boolean" },
      { key: "seek_seconds", label: "Seek", kind: "duration" },
      { key: "start_segment_number", label: "Start segment" },
      { key: "segment_generation", label: "Generation" },
      { key: "last_requested_segment", label: "Last requested" },
      { key: "last_completed_segment", label: "Last delivered" },
      { key: "restart_count", label: "Restarts" },
      { key: "total_duration_seconds", label: "Media length", kind: "duration" },
    ],
  },
  {
    title: "Paths",
    fields: [
      { key: "ffmpeg_path", label: "FFmpeg", wide: true },
      { key: "input_path", label: "Input", wide: true },
      { key: "output_dir", label: "Output", wide: true },
    ],
  },
];

/** Build the readable sections and raw payload from the newest FFmpeg facts. */
export function buildTranscodeDebugModel(
  session: AdminSession,
  rows: OperationalLogEntry[],
): TranscodeDebugModel {
  const attrs = collectTranscodeDebugAttrs(session, rows);
  return {
    attrs,
    command: formatFFmpegCommand(attrs),
    sections: DEBUG_SECTIONS.map(({ title, fields }) => ({
      title,
      facts: fields.flatMap((field) => {
        const value = formatDebugValue(attrs[field.key], field.kind);
        return value === null
          ? []
          : [{ key: field.key, label: field.label, value, wide: field.wide }];
      }),
    })).filter((section) => section.facts.length > 0),
  };
}

/** Merge newest-first log attributes, then fill gaps from the live session. */
export function collectTranscodeDebugAttrs(
  session: AdminSession,
  rows: OperationalLogEntry[],
): Record<string, unknown> {
  const attrs: Record<string, unknown> = {};

  for (const row of rows) {
    if (row.node_id && attrs.node_id === undefined) {
      attrs.node_id = row.node_id;
    }
    for (const [key, value] of Object.entries(row.attrs ?? {})) {
      if (attrs[key] === undefined && value !== undefined && value !== "") {
        attrs[key] = value;
      }
    }
  }

  const sessionFallbacks: Record<string, unknown> = {
    target_resolution: session.target_resolution,
    target_video_codec: session.target_video_codec,
    target_audio_codec: session.target_audio_codec,
    target_audio_channels: session.target_audio_channels,
    target_bitrate_kbps: session.target_bitrate_kbps,
    hw_accel: session.transcode_hw_accel,
    tone_map_mode: session.tone_map_mode,
    source_video_codec: session.source_video_codec,
    source_video_resolution: session.source_video_resolution,
    source_audio_channels: session.source_audio_channels,
  };

  for (const [key, value] of Object.entries(sessionFallbacks)) {
    if (attrs[key] === undefined) {
      attrs[key] = value;
    }
  }

  return Object.fromEntries(
    Object.entries(attrs)
      .filter(([, value]) => value !== undefined && value !== null && value !== "")
      .sort(([left], [right]) => left.localeCompare(right)),
  );
}

/** Render the captured binary and argument vector without losing boundaries. */
export function formatFFmpegCommand(attrs: Record<string, unknown>): string | null {
  const path = typeof attrs.ffmpeg_path === "string" ? attrs.ffmpeg_path.trim() : "";
  const args = Array.isArray(attrs.ffmpeg_args)
    ? attrs.ffmpeg_args.filter((value): value is string => typeof value === "string")
    : [];
  if (!path && args.length === 0) {
    return null;
  }
  return [path || "ffmpeg", ...args].map(quoteCommandPart).join(" ");
}

/** Quote one argument for lossless copy and paste into a POSIX shell. */
function quoteCommandPart(value: string): string {
  return /^[A-Za-z0-9_./:=+,-]+$/.test(value) ? value : `'${value.replace(/'/g, `'"'"'`)}'`;
}

function formatDebugValue(value: unknown, kind: DebugField["kind"]): string | null {
  if (value === undefined || value === null || value === "") {
    return null;
  }
  switch (kind) {
    case "audio_track": {
      const index = numericValue(value);
      return index === null ? String(value) : index < 0 ? "Default" : String(index);
    }
    case "bit_depth": {
      const bits = numericValue(value);
      return bits === null ? String(value) : bits > 0 ? `${bits}-bit` : null;
    }
    case "boolean":
      return value === true ? "Yes" : value === false ? "No" : String(value);
    case "bitrate": {
      const bitrate = numericValue(value);
      if (bitrate === null) return String(value);
      return bitrate > 0 ? `${bitrate.toLocaleString()} kbps` : "Quality based (uncapped)";
    }
    case "channels": {
      const channels = numericValue(value);
      if (channels === null) return String(value);
      return channels > 0 ? String(channels) : "Unknown";
    }
    case "duration": {
      const seconds = numericValue(value);
      return seconds === null ? String(value) : `${seconds.toLocaleString()} s`;
    }
    case "pipeline":
      return formatPipeline(String(value));
    case "pixel_formats":
      return Array.isArray(value) ? value.map(String).join(" → ") : String(value);
    case "subtitle_track": {
      const index = numericValue(value);
      return index === null ? String(value) : index < 0 ? "None" : String(index);
    }
    default:
      return Array.isArray(value) ? value.map(String).join(", ") : String(value);
  }
}

function numericValue(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value !== "string" || value.trim() === "") return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function formatPipeline(value: string): string {
  switch (value) {
    case "hardware_decode_hardware_encode":
      return "GPU decode → GPU encode";
    case "software_decode_hardware_encode":
      return "CPU decode → GPU encode";
    case "software_decode_software_encode":
      return "CPU decode → CPU encode";
    case "video_copy_audio_transcode":
      return "Video copy + audio transcode";
    case "remux":
      return "Remux / stream copy";
    default:
      return value;
  }
}
