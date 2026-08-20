/**
 * Playback protocol v3 wire contract.
 *
 * These types mirror the Go structs in `internal/playback/protocol_v3.go`
 * field-for-field, including JSON tag names and optionality. The normative
 * specification is `docs/architecture/playback-protocol-v3.md`; the golden
 * fixtures under `internal/playback/testdata/protocol_v3/` are the shared
 * cross-platform conformance corpus.
 *
 * Nothing here is web-specific: the file is the contract, not the client's
 * use of it. Web-side construction of these payloads lives in
 * `client-context-v3.ts`.
 */

/** Protocol version carried by every v3 request and response. */
export const PROTOCOL_V3 = 3;

/** Maximum number of opaque plan keys a recovery request may carry. */
export const MAX_ATTEMPTED_PLAN_KEYS_V3 = 16;

/** Maximum number of failed plans in one recovery chain. */
export const MAX_ATTEMPT_COUNT_V3 = 8;

/**
 * How the server delivers the stream. Clients negotiate in delivery *classes*
 * (see {@link DeliveryClassV3}); these finer-grained values only appear on a
 * plan, describing what the server actually did.
 */
export type DeliveryV3 =
  | "original_http"
  | "server_remux_progressive"
  | "server_remux_hls"
  | "server_transcode_hls";

/**
 * The client-side negotiation unit. A client advertises the classes it can
 * execute in {@link ClientPlaybackContextV3.deliveries} and maps each onto its
 * own player internally. An omitted class is unavailable.
 */
export type DeliveryClassV3 = "original_http" | "progressive" | "hls";

/** Folds a plan's delivery onto the class the client advertised for it. */
export function deliveryClassV3(delivery: DeliveryV3): DeliveryClassV3 {
  switch (delivery) {
    case "original_http":
      return "original_http";
    case "server_remux_progressive":
      return "progressive";
    case "server_remux_hls":
    case "server_transcode_hls":
      return "hls";
  }
}

/**
 * How much the server may trust a client's decode claims.
 *
 * - `exact` — per-codec profiles/levels/bit-depths/bounds from a real platform
 *   probe (Android MediaCodecList). Full strict validation.
 * - `platform_attested` — platform-level decoder attestation without
 *   profile/level enumeration (Apple VideoToolbox). Codec, resolution, bit
 *   depth, frame rate, and dynamic range are validated; profile/level matching
 *   is skipped instead of failing conservative.
 * - `declared` — boolean support statements (web `MediaSource.isTypeSupported`).
 *   Copy routes are granted on codec+container+range match from the flat codec
 *   lists; no strict direct claims are made.
 */
export type CapabilityEvidenceV3 = "exact" | "platform_attested" | "declared";

/** Which replan intent a replan request expresses. */
export type ReplanOperationV3 =
  | "failure_recovery"
  | "seek_reanchor"
  | "seek_failure_recovery"
  | "track_change"
  | "quality_change"
  | "output_change";

/** Transport protocol of a plan's stream URL. */
export type StreamProtocolV3 = "http_progressive" | "hls";

/** Whether the plan's stream headers need periodic refresh. */
export type HeaderRefreshModeV3 = "none" | "session";

/** Whether the server produced a playable plan or gave up. */
export type DecisionOutcomeV3 = "playable" | "adaptation_unavailable";

/** How the client should restore position after adopting a new stream. */
export type SeekRestorationV3 = "player_position" | "source_position";

/** What the server decided to do with subtitles for this plan. */
export type SubtitleModeV3 = "off" | "render" | "convert" | "burn_in";

/** How much subtitle fidelity the client would rather keep. */
export type SubtitleFidelityV3 = "preserve" | "compatible";

/** Which side owns durable item resume/history persistence. */
export type ProgressPersistenceV3 = "server" | "client";

/** Presence and kind of a Dolby Vision enhancement layer on the source. */
export type EnhancementLayerV3 = "none" | "mel" | "fel" | "unknown";

/** Whether the client or the server performs a transformation. */
export type TransformationExecutorV3 = "client" | "server";

/** Route-event names the server accepts as playback diagnostics. */
export type RouteEventNameV3 =
  | "plan_selected"
  | "plan_invalidated"
  | "plan_failed"
  | "first_frame"
  | "terminal"
  | "stopped"
  | "runtime_correction_applied"
  | "runtime_correction_succeeded"
  | "runtime_correction_failed"
  | "seek_reanchor_requested"
  | "seek_reanchored";

/** Feature name the client advertises when it can consume a v3 plan. */
export const FEATURE_PLAYBACK_PLAN_V3 = "playback_plan_v3";

/** Server-minted attempt keys and intent replans from the neutral v3 contract. */
export const FEATURE_NEUTRAL_PLAYBACK_V3_CONTRACT = "neutral_playback_v3_contract_v1";

/** Server accepts output-capability refreshes without treating the route as failed. */
export const FEATURE_OUTPUT_CHANGE_V3 = "output_change_v1";

/** The `original` rung label, which always preserves the source. */
export const QUALITY_ORIGINAL_V3 = "original";

/** Server-executed transformation names a plan can list. */
export const TRANSFORMATION_AUDIO_TO_AAC_V3 = "audio_to_aac";
export const TRANSFORMATION_VIDEO_TO_H264_V3 = "video_to_h264";
export const TRANSFORMATION_SERVER_DV7_TO_HDR10_V3 = "server_dv7_to_hdr10";

// ---------------------------------------------------------------------------
// Request-side capability description
// ---------------------------------------------------------------------------

export interface HDRCapabilitiesV3 {
  hdr10: boolean;
  hdr10_plus: boolean;
  hlg: boolean;
  hdr10_max_width?: number;
  hdr10_max_height?: number;
  hdr10_max_frame_rate?: number;
  hdr10_max_bitrate_kbps?: number;
  dolby_vision_profiles: number[];
  dolby_vision_profile_levels?: Array<{
    profile: number;
    max_level: number;
    bl_compatibility_ids?: number[];
  }>;
}

export interface AudioPassthroughEntryV3 {
  codec: string;
  channel_counts?: number[];
  layouts?: string[];
}

export interface AudioPassthroughV3 {
  passthrough_codecs: string[];
  spatializer_enabled: boolean;
  max_channels: number;
  entries?: AudioPassthroughEntryV3[];
}

export interface VideoDecodeCapabilityV3 {
  codec: string;
  decoder_name?: string;
  profiles?: string[];
  levels?: number[];
  bit_depths?: number[];
  max_width?: number;
  max_height?: number;
  max_frame_rate?: number;
  max_bitrate_kbps?: number;
  hardware: boolean;
}

export interface ClientCodecCapabilitiesV3 {
  video_evidence: CapabilityEvidenceV3;
  audio_evidence: CapabilityEvidenceV3;
  codecs_video: string[];
  codecs_video_hardware: string[];
  codecs_audio: string[];
  containers: string[];
  max_resolution?: string;
  hdr: boolean;
  hdr_details?: HDRCapabilitiesV3;
  audio_passthrough?: AudioPassthroughV3;
  video_decode?: VideoDecodeCapabilityV3[];
}

export interface DeviceContextV3 {
  platform?: string;
  os_version?: string;
  manufacturer?: string;
  model?: string;
  platform_details?: Record<string, string>;
}

export interface OutputContextV3 {
  hdr_details?: HDRCapabilitiesV3;
  audio_passthrough?: AudioPassthroughV3;
  current_sink?: string;
  sink_type?: string;
  /**
   * Opaque token identifying the current output route. The server only ever
   * compares it for equality, so any stable platform-native identity works.
   * Web omits it — a browser has no stable route identity to report.
   */
  output_context_id?: string;
}

export interface DeliverySubtitleCapabilitiesV3 {
  embedded_text: boolean;
  sidecar_text: boolean;
  ass_styling: boolean;
  embedded_bitmap: boolean;
  sidecar_bitmap: boolean;
  font_attachments: boolean;
}

export interface TransformationV3 {
  name: string;
  executor: TransformationExecutorV3;
  recipe_version: string;
  validated_claims: string[];
}

export interface DeliveryCapabilityV3 {
  enabled: boolean;
  supported_on_device: boolean;
  failure_reason?: string;
  containers: string[];
  video_codecs: string[];
  audio_decode_codecs: string[];
  audio_passthrough_codecs: string[];
  max_channels?: number;
  hdr_details?: HDRCapabilitiesV3;
  subtitles: DeliverySubtitleCapabilitiesV3;
  features: string[];
  auth_header_refresh: boolean;
  validated_claims: string[];
  transformations: TransformationV3[];
}

export interface ClientPlaybackContextV3 {
  protocol_version: number;
  form_factor: string;
  app_version: string;
  /**
   * Opaque per-platform build identifier and distribution channel — the
   * body-level fallback for the `X-Silo-Client-Build` / `X-Silo-Client-Channel`
   * headers. The server stores both verbatim and never parses or compares
   * them. The web player has no build concept and omits them.
   */
  app_build?: string;
  app_channel?: string;
  device: DeviceContextV3;
  output: OutputContextV3;
  /**
   * Keyed by {@link DeliveryClassV3}. Both `enabled` and `supported_on_device`
   * must be true for a class to be eligible; an omitted class is unavailable.
   */
  deliveries: Partial<Record<DeliveryClassV3, DeliveryCapabilityV3>>;
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

export interface StartRequestV3 {
  protocol_version: number;
  client_features: string[];
  file_id: number;
  profile_id: string;
  playback_attempt_id: string;
  quality_preference: string;
  subtitle_fidelity_preference: SubtitleFidelityV3;
  start_position?: number;
  progress_persistence?: ProgressPersistenceV3;
  audio_track_id?: string;
  audio_track_index?: number;
  subtitle_track_id?: string;
  subtitle_track_index?: number;
  metered: boolean;
  bandwidth_estimate_kbps?: number;
  bandwidth_cap_kbps?: number;
  client_capabilities: ClientCodecCapabilitiesV3;
  client_playback_context: ClientPlaybackContextV3;
}

export interface TrackIdentityV3 {
  id: string;
  index?: number;
}

export interface SelectedTracksV3 {
  audio?: TrackIdentityV3;
  subtitle?: TrackIdentityV3;
}

export interface FailureV3 {
  classification: string;
  message?: string;
  decoder_name?: string;
}

export interface ReplanRequestV3 {
  protocol_version: number;
  client_features?: string[];
  operation?: ReplanOperationV3;
  playback_attempt_id: string;
  replan_request_id: string;
  failed_plan_id: string;
  plan_attempt_id: string;
  plan_attempt_key: string;
  attempted_plan_keys: string[];
  local_mutations?: string[];
  attempt_count: number;
  quality_preference: string;
  position_seconds: number;
  metered: boolean;
  bandwidth_estimate_kbps?: number;
  bandwidth_cap_kbps?: number;
  selected_tracks: SelectedTracksV3;
  failure?: FailureV3;
  client_capabilities: ClientCodecCapabilitiesV3;
  client_playback_context: ClientPlaybackContextV3;
}

export interface RouteEventV3 {
  protocol_version: number;
  playback_attempt_id: string;
  session_id?: string;
  plan_id?: string;
  plan_attempt_id?: string;
  plan_attempt_key?: string;
  event: RouteEventNameV3;
  failure_classification?: string;
  fallback_reason?: string;
  applied_quirk_ids?: string[];
  quirk_registry_revision?: string;
  output_context_id?: string;
  diagnostics: Record<string, string>;
}

// ---------------------------------------------------------------------------
// Plan
// ---------------------------------------------------------------------------

export interface StreamV3 {
  url: string;
  protocol: StreamProtocolV3;
  container?: string;
  mime_type?: string;
  headers: Record<string, string>;
  header_refresh: HeaderRefreshModeV3;
  header_refresh_url?: string;
}

export interface TimelineV3 {
  source_start_seconds: number;
  stream_origin_seconds: number;
  player_start_seconds: number;
  timeline_offset_seconds: number;
  seek_window_start_seconds?: number;
  seek_window_end_seconds?: number;
  can_seek_anywhere: boolean;
  seek_restoration: SeekRestorationV3;
}

export interface EffectiveRecipeV3 {
  video_codec?: string;
  audio_codec?: string;
  width?: number;
  height?: number;
  frame_rate?: number;
  bitrate_kbps?: number;
  dynamic_range?: string;
  audio_channels?: number;
  audio_layout?: string;
}

export interface SourceDescriptorV3 {
  media_file_id: number;
  /**
   * Full runtime of this source, independent of where the delivery's timeline
   * is anchored. Absent means the server does not know the runtime — it is
   * omitted rather than sent as null so a client cannot coerce it to zero. A
   * client must not substitute the playback engine's reported duration: on an
   * HLS copy remux the engine reports the length produced so far.
   */
  duration_seconds?: number;
  container?: string;
  video_codec?: string;
  video_profile?: string;
  video_level?: number;
  bit_depth?: number;
  color_range?: string;
  width?: number;
  height?: number;
  frame_rate?: number;
  bitrate_kbps?: number;
  dynamic_range?: string;
  hdr10_plus: boolean;
  dolby_vision_profile?: number;
  dolby_vision_level?: number;
  dv_bl_compat_id?: number;
  dv_enhancement_layer: EnhancementLayerV3;
  audio_codec?: string;
  audio_channels?: number;
  audio_layout?: string;
  video_copy_unsafe?: boolean;
}

export interface VideoClaimsV3 {
  hdr10: boolean;
  hdr10_plus: boolean;
  hlg: boolean;
  dolby_vision: boolean;
  dolby_vision_reason?: string;
}

export interface AudioClaimsV3 {
  codec?: string;
  passthrough: boolean;
  atmos_preserved: boolean;
  dts_variant?: string;
  reason?: string;
}

export interface SubtitleClaimsV3 {
  ass_styling_preserved: boolean;
  bitmap_overlay: boolean;
  bitmap_sidecar: boolean;
  reason?: string;
}

export interface ValidationClaimsV3 {
  video: VideoClaimsV3;
  audio: AudioClaimsV3;
  subtitles: SubtitleClaimsV3;
}

export interface SubtitleArtifactV3 {
  url: string;
  mime_type: string;
  format: string;
  timing_origin_seconds: number;
}

/** How a single inventory track can be delivered to the client. */
export type SubtitleDeliveryV3 = "sidecar" | "burn_in_only";

export interface SubtitleInventoryItemV3 {
  track_id: string;
  /**
   * Position in the server's dense, gap-free combined ordinal space: external
   * sidecars first, then embedded container tracks, then downloaded/generated
   * tracks. Every track occupies an ordinal, including bitmap tracks published
   * with `burn_in_only` and no URL.
   */
  combined_index: number;
  source: string;
  codec?: string;
  language?: string;
  label?: string;
  forced: boolean;
  default: boolean;
  hearing_impaired: boolean;
  delivery: SubtitleDeliveryV3;
  url?: string;
  font_bundle_url?: string;
}

export interface SubtitleDecisionV3 {
  mode: SubtitleModeV3;
  track_id?: string;
  artifact?: SubtitleArtifactV3;
  /**
   * The complete, gap-free combined-ordinal subtitle track list for the
   * effective source. It is authoritative: select a track by echoing an
   * entry's `track_id` or `combined_index`, never by counting, summing track
   * arrays, or taking `max(index) + 1`.
   */
  inventory: SubtitleInventoryItemV3[];
}

export interface AppliedQuirkV3 {
  id: string;
  registry_revision: string;
  action: string;
  reason?: string;
}

export interface DegradationWarningV3 {
  code: string;
  message: string;
}

/**
 * One server-ladder rung valid for this source and client, published so the
 * client can render a quality menu without owning a bitrate table. The
 * `original` entry preserves the source.
 */
export interface AvailableQualityV3 {
  label: string;
  height?: number;
  bitrate_kbps?: number;
  preserves_source: boolean;
}

export interface PlanV3 {
  protocol_version: number;
  plan_id: string;
  plan_attempt_key: string;
  session_id?: string;
  expires_at?: string;
  delivery: DeliveryV3;
  stream: StreamV3;
  timeline: TimelineV3;
  selected_tracks: SelectedTracksV3;
  effective_recipe: EffectiveRecipeV3;
  claims: ValidationClaimsV3;
  subtitle: SubtitleDecisionV3;
  transformations: TransformationV3[];
  applied_quirks: AppliedQuirkV3[];
  runtime_corrections: string[];
  available_qualities: AvailableQualityV3[];
  degradation_warnings: DegradationWarningV3[];
  decision_reason: string;
  requested_media_file_id: number;
  effective_media_file_id: number;
  source: SourceDescriptorV3;
  subtitle_fidelity_policy: string;
}

export interface TerminalV3 {
  reason: string;
  message: string;
  retryable: boolean;
}

export interface DecisionResponseV3 {
  protocol_version: number;
  server_features: string[];
  outcome: DecisionOutcomeV3;
  session_id?: string;
  playback_plan?: PlanV3;
  terminal?: TerminalV3;
}

export interface CapabilityResponseV3 {
  enabled: true;
  protocol_versions: number[];
  features: string[];
  deliveries: DeliveryV3[];
  transformations: TransformationV3[];
  reason?: string;
}

// ---------------------------------------------------------------------------
// Contract helpers
// ---------------------------------------------------------------------------

/** True when both flags make the advertised delivery class eligible. */
export function deliveryAvailableV3(capability: DeliveryCapabilityV3 | undefined): boolean {
  return capability != null && capability.enabled && capability.supported_on_device;
}

/**
 * Looks up an inventory entry by its combined ordinal. Returns undefined when
 * the ordinal is not published — clients must never synthesize one.
 */
export function subtitleInventoryItemAtV3(
  inventory: SubtitleInventoryItemV3[],
  combinedIndex: number,
): SubtitleInventoryItemV3 | undefined {
  return inventory.find((item) => item.combined_index === combinedIndex);
}
