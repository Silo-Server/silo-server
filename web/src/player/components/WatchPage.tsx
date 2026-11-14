import { isSourceFallbackReason } from "@/api/v2/watchTogetherSourceFallback";
import { playbackCapabilitiesV2 } from "../start-v2";
import { playerV2Origin } from "../player-v2";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import type { PlayerFileVersion, PlayerPlaybackStateChange, WatchPageProps } from "../types";
import type { PlayerIndexerRelease, PlayerPlaybackVariant } from "../types";
import type {
  PlaybackRealtimeEventEnvelope,
  PlaybackSourceCommittedPayload,
} from "../realtime-protocol";
import type { SubtitleInventoryItemV3 } from "../protocol-v3";
import { usePlaybackSession } from "../hooks/usePlaybackSession";
import { isAdaptationUnavailableReason } from "../playback-errors";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import {
  hasSelectableSessionSubtitles,
  resolvePlayableSubtitles,
  resolveSubtitleMenuTracks,
} from "../utils/playableSubtitles";
import { isOverlayInventoryProvisional } from "../utils/inventoryProvenance";
import { patchVersionMarkers, resolveActiveVersionMarkers } from "../utils/watchPageMarkers";
import { buildPublishedSubtitleTracks } from "../utils/subtitleInventory";
import {
  buildSubtitleChoiceRequests,
  sendSubtitleChoiceRequest,
} from "../utils/subtitleChoicePersistence";
import {
  resolveEffectiveVersion,
  type EffectiveVersionIdentity,
} from "../utils/resolveEffectiveVersion";
import { VideoPlayer } from "./VideoPlayer";
import { fetchWatchDetail } from "@/hooks/queries/items";
import {
  applyVersionAvailability,
  type VersionLivenessVerdict,
} from "@/hooks/queries/versionLiveness";
import {
  awaitVirtualCandidatesRefresh,
  cancelVirtualCandidatesRefresh,
} from "@/api/v2/mediaCandidates";
import { transientTrickplayError, useWatchTrickplay } from "@/hooks/queries/trickplay";
import { itemKeys } from "@/hooks/queries/keys";
import { useRealtimeEvents } from "@/components/realtimeEventsContext";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useWatchTogetherRoomConnection } from "../hooks/useWatchTogetherRoomConnection";
import { toast } from "sonner";

/**
 * Live inventory refresh. A session that started before the server finished
 * probing a virtual file carries the synthesized inventory the plan had then.
 * These bound how often the client re-reads the catalog to fill the menus in.
 * The poll only ever updates menu data; it never restarts the stream.
 */
export const INVENTORY_REFRESH_INTERVAL_MS = 20_000;
export const INVENTORY_REFRESH_MAX_ATTEMPTS = 5;
// Wall-clock backstop: failed requests do not count toward the attempt cap, so
// a persistent error loop also needs an absolute deadline to stop at.
export const INVENTORY_REFRESH_DEADLINE_MS = 5 * 60_000;
// Early cadence for the first attempts. A virtual file's probe typically
// persists its inventory within seconds of the optimistic start, so the first
// reads must not wait a full 20 s interval to see it.
export const INVENTORY_REFRESH_EARLY_DELAYS_MS: readonly number[] = [2_000, 4_000, 8_000];
// Must match `useWatchDetail`'s staleTime so the inventory poll, chapter
// refresh, and realtime marker reconcile share the mounted query's cache
// instead of each issuing an independent fetch.
const WATCH_DETAIL_STALE_TIME_MS = 30_000;

/**
 * Delay before the next inventory poll. `attempt` is the number of polls
 * already scheduled, so 0 yields the first early delay. Once the inventory is
 * found the poll is complete and the delay is 0 (no further poll).
 */
export function inventoryPollDelayMs(attempt: number, found: boolean): number {
  if (found) return 0;
  return INVENTORY_REFRESH_EARLY_DELAYS_MS[attempt] ?? INVENTORY_REFRESH_INTERVAL_MS;
}

function patchChapterThumbnail(
  versions: PlayerFileVersion[],
  fileId: number,
  chapterIndex: number,
  thumbnailUrl: string,
  thumbnailThumbhash?: string,
): PlayerFileVersion[] {
  let changed = false;
  const nextVersions = versions.map((version) => {
    if (version.file_id !== fileId || !version.chapters?.length) {
      return version;
    }

    let versionChanged = false;
    const nextChapters = version.chapters.map((chapter) => {
      if (chapter.index !== chapterIndex) {
        return chapter;
      }
      if (
        chapter.thumbnail_url === thumbnailUrl &&
        chapter.thumbnail_thumbhash === thumbnailThumbhash
      ) {
        return chapter;
      }
      changed = true;
      versionChanged = true;
      return {
        ...chapter,
        thumbnail_url: thumbnailUrl,
        thumbnail_thumbhash: thumbnailThumbhash,
      };
    });

    return versionChanged ? { ...version, chapters: nextChapters } : version;
  });

  return changed ? nextVersions : versions;
}

/**
 * Short human label for the catalogue row the plan landed on, used to name the
 * substituted source in the version-swap notice. Returns null when the row
 * carries nothing recognizable, so the notice can fall back to generic copy.
 */
function buildEffectiveVersionLabel(version: PlayerFileVersion): string | null {
  const video = version.codec_video ? version.codec_video.toUpperCase() : "";
  const parts = [version.resolution, video].filter((part) => part.trim().length > 0);
  if (parts.length === 0) {
    return null;
  }
  return `${parts.join(" ")}${version.hdr ? " HDR" : ""}`;
}

/**
 * Re-keys the resolved row for the live session file into the version list.
 *
 * A start or an in-player switch can leave the session playing a file the
 * item's version list does not name: the server substituted another version,
 * or the plan resolved a virtual candidate the list carries under a different
 * row. Lookups keyed on `session.mediaFileId` then miss, so `activeVersion`
 * falls through to another release's first row and `isVirtualActiveFile`,
 * `selectedDuration` and `activeChapters` lose the live file's data. Resolve
 * the row for the live file by the plan's published effective source first,
 * otherwise by id, and re-key it under the session's file id, appending when
 * the list carries none.
 *
 * When the list already carries the live file id, the resolved row must be that
 * same file to replace it: a serve-layer rotation can move the effective source
 * without changing the collapsed file id, leaving a row whose URI is stale.
 * A path match that lands on a different catalog row (a virtual collapsed row
 * resolved to a concrete candidate) keeps the live row for provenance — the
 * substitution notice reads its identity — and appends the candidate under its
 * own id when the refreshed list omitted it. The list is returned unchanged when
 * the live row cannot be resolved.
 */
export function mergeResolvedLiveVersion(
  versions: PlayerFileVersion[],
  liveFileId: number | null,
  identity: EffectiveVersionIdentity,
  candidates: readonly PlayerFileVersion[],
): PlayerFileVersion[] {
  if (liveFileId == null) return versions;
  const liveIdentity = { ...identity, mediaFileId: liveFileId };
  // Match the committed source by its published URI across both lists before
  // falling back to the collapsed id: within a single list the id fallback
  // would otherwise re-select the stale live row and hide the rotation.
  const committedUri = identity.effectiveVirtualUri;
  const resolved =
    (committedUri
      ? (versions.find((version) => version.file_path === committedUri) ??
        candidates.find((version) => version.file_path === committedUri))
      : undefined) ??
    resolveEffectiveVersion(versions, liveIdentity) ??
    resolveEffectiveVersion(candidates, liveIdentity);
  if (!resolved) return versions;
  const index = versions.findIndex((version) => version.file_id === liveFileId);
  if (index === -1) return [...versions, { ...resolved, file_id: liveFileId }];
  if (resolved.file_id !== liveFileId) {
    // The committed source is a different catalog row (a virtual collapsed row
    // resolved to a concrete candidate). Keep the live row for provenance, and
    // append the candidate under its own id when the refreshed list omitted it.
    if (versions.some((version) => version.file_id === resolved.file_id)) return versions;
    return [...versions, resolved];
  }
  if (versions[index] === resolved) return versions;
  const next = versions.slice();
  next[index] = { ...resolved, file_id: liveFileId };
  return next;
}

/**
 * Turns the server's additive `substitution_reason` into the clause the notice
 * leads with. An unknown or absent reason yields null so the caller falls back
 * to the generic substitution copy rather than inventing a cause.
 */
function substitutionReasonPhrase(reason: string | undefined): string | null {
  switch (reason) {
    case "dead_release":
      return "The selected version is no longer available";
    case "listing_failed":
      return "The provider couldn't list the selected version";
    case "transport_failed":
      return "The selected version wouldn't start";
    case "decode_rejected":
      return "This device can't decode the selected version";
    case "version_not_playable":
      return "The chosen version could not be played";
    default:
      return null;
  }
}

/**
 * WatchPage is the top-level player component.
 * Starts a playback session, then renders the VideoPlayer once the stream is ready.
 */
export function WatchPage(props: WatchPageProps) {
  const config = usePlayerConfig();
  return props.watchTogetherRoomId ? (
    <WatchPartyPlaybackGate
      key={`${playerV2Origin(config)}:${props.watchTogetherRoomId}`}
      {...props}
    />
  ) : (
    <WatchPagePlayer {...props} />
  );
}

function WatchPartyPlaybackGate(props: WatchPageProps) {
  const config = usePlayerConfig();
  const [status, setStatus] = useState<"checking" | "supported" | "unsupported" | "failed">(
    "checking",
  );
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let cancelled = false;
    void playbackCapabilitiesV2(config)
      .then(({ features }) => {
        if (!cancelled) {
          setStatus(
            features.includes("watch_party_coordinator_v1") &&
              features.includes("fixed_media_file_v1")
              ? "supported"
              : "unsupported",
          );
        }
      })
      .catch(() => {
        if (!cancelled) setStatus("failed");
      });
    return () => {
      cancelled = true;
    };
  }, [config, attempt]);

  if (status === "supported") return <WatchPagePlayer {...props} />;
  return (
    <div className="bg-background fixed inset-0 z-50 flex items-center justify-center px-6">
      <div className="surface-panel-subtle flex max-w-md flex-col items-center gap-4 rounded-[1.8rem] px-8 py-8 text-center">
        <p className="text-base font-semibold text-white">
          {status === "checking" ? "Checking Watch Party support..." : "Watch Party unavailable"}
        </p>
        {status !== "checking" && (
          <p className="text-sm text-white/60">
            {status === "unsupported"
              ? "This server needs an update to support Watch Party."
              : "Unable to check Watch Party support. Please try again."}
          </p>
        )}
        {status === "failed" && (
          <button
            type="button"
            className="rounded-[0.95rem] bg-white/10 px-4 py-2 text-sm font-medium text-white"
            onClick={() => {
              setStatus("checking");
              setAttempt((value) => value + 1);
            }}
          >
            Try Again
          </button>
        )}
        <button
          type="button"
          className="rounded-[0.95rem] bg-white/10 px-4 py-2 text-sm font-medium text-white"
          onClick={() => {
            void props.onExit();
          }}
        >
          Go Back
        </button>
      </div>
    </div>
  );
}

// Stable empty reference: a fresh `[]` default would give the indexer-release
// sync effect a new identity on every render and loop it.
const EMPTY_INDEXER_RELEASES: PlayerIndexerRelease[] = [];

// Stable empty reference for the same reason as `EMPTY_INDEXER_RELEASES`: the
// array identity reaches the session hook's `selectFileId` and the legacy
// catalog poll's dependency set, so a fresh `[]` default on every render would
// restart the poll's attempt budget before it can spend it and clear the
// terminal failure badge.
const EMPTY_PLAYBACK_VARIANTS: PlayerPlaybackVariant[] = [];

// The liveness verdicts are optional so WatchPage can render without the
// item-page check (watch-party tests, standalone renders). An empty map leaves
// every row's own metadata flag untouched.
const EMPTY_VERSION_LIVENESS = new Map<number, VersionLivenessVerdict>();

function WatchPagePlayer({
  contentId,
  title,
  year,
  playbackRequestKey,
  fileId,
  libraryId,
  versions,
  versionLiveness = EMPTY_VERSION_LIVENESS,
  playbackVariants = EMPTY_PLAYBACK_VARIANTS,
  indexerReleases = EMPTY_INDEXER_RELEASES,
  virtualRanking,
  subtitles,
  initialPosition,
  forceInitialPosition,
  qualityPreference,
  maxBitrateKbps,
  explicitAudioTrackIndex,
  initialSubtitleTrackIndexByFileId,
  initialBitmapSubtitleTrackIndexByFileId,
  explicitFileSelection = false,
  forceRelink = false,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  subtitleMode,
  showForcedSubtitles,
  profileLanguage,
  introSkipMode,
  autoSkipRecap,
  autoPlayNextPreview,
  canEditMarkers,
  seriesContext,
  onNavigateEpisode,
  onEnded,
  onRequestedVersionChange,
  onExit,
  onMinimize,
  resumeHints,
  displayMode,
  onPictureInPictureChange,
  autoEnterPictureInPicture,
  onPlaybackStateChange,
  onPlaybackTransportReady,
  seekIntervals,
  onReturnFromPostRoll,
  watchTogetherRoomId,
  watchTogetherRoomToken,
}: WatchPageProps) {
  const config = usePlayerConfig();
  const queryClient = useQueryClient();
  const playbackController = useWatchPlaybackController();
  const chapterRefreshAttemptsRef = useRef<Set<number>>(new Set());
  const handledSelectionRevisionRef = useRef<number | null>(null);
  const playbackPositionRef = useRef(initialPosition ?? 0);
  const [playbackVersions, setPlaybackVersions] = useState(versions);
  const [indexerReleaseRows, setIndexerReleaseRows] = useState(indexerReleases);
  const [versionSwapNoticeDismissed, setVersionSwapNoticeDismissed] = useState(false);
  const [realtimeConnectionState, setRealtimeConnectionState] = useState<
    "disconnected" | "connecting" | "connected"
  >("disconnected");
  // Only a reconnection needs the player to re-read its watch detail. The
  // first "connected" is the socket this player just opened, at the same moment
  // the mounted detail query fetched; reconciling there duplicated that read.
  // An epoch counts reconnections and the reconcile below consumes it once.
  const realtimeConnectedOnceRef = useRef(false);
  const [realtimeReconnectEpoch, setRealtimeReconnectEpoch] = useState(0);
  useEffect(() => {
    if (realtimeConnectionState !== "connected") return;
    if (!realtimeConnectedOnceRef.current) {
      realtimeConnectedOnceRef.current = true;
      return;
    }
    setRealtimeReconnectEpoch((epoch) => epoch + 1);
  }, [realtimeConnectionState]);
  const watchTogetherConnection = useWatchTogetherRoomConnection({
    roomId: watchTogetherRoomId,
    roomToken: watchTogetherRoomToken,
  });

  useEffect(() => {
    setPlaybackVersions(versions);
  }, [versions]);

  useEffect(() => {
    setIndexerReleaseRows(indexerReleases);
  }, [indexerReleases]);

  const session = usePlaybackSession(
    playbackRequestKey ??
      JSON.stringify([contentId, fileId ?? null, initialPosition, forceInitialPosition]),
    playbackVersions,
    playbackVariants,
    fileId,
    initialPosition,
    forceInitialPosition,
    qualityPreference,
    maxBitrateKbps,
    resumeHints,
    explicitAudioTrackIndex,
    initialSubtitleTrackIndexByFileId,
    initialBitmapSubtitleTrackIndexByFileId,
    explicitFileSelection,
    forceRelink,
    !watchTogetherRoomId,
    subtitleMode,
  );

  const sessionRef = useRef(session);
  sessionRef.current = session;

  // A manual retry from a stalled track menu re-arms the bounded discovery
  // rounds below and makes one immediate inventory read. The menus restart
  // their own bound, so the retry shows progress instead of failing at once.
  const [inventoryRetryNonce, setInventoryRetryNonce] = useState(0);
  const handleRetryInventory = useCallback(() => {
    sessionRef.current.markInventoryRetrying();
    setInventoryRetryNonce((current) => current + 1);
    void sessionRef.current.pollInventory();
  }, []);

  const fallbackHandledRef = useRef<string | null>(null);
  const [pendingFallbackKey, setPendingFallbackKey] = useState<string | null>(null);
  const fallbackRoom = watchTogetherConnection.room;
  const fallbackSource = watchTogetherConnection.fallbackSource;
  const fallbackReason = session.errorReason;
  const fallbackKey =
    watchTogetherRoomId &&
    watchTogetherRoomToken &&
    fallbackRoom &&
    fileId === fallbackRoom.selected_file_id &&
    isSourceFallbackReason(fallbackReason)
      ? `${watchTogetherRoomId}:${watchTogetherRoomToken}:${fallbackRoom.selection_revision}:${fileId}:${session.playbackAttemptId}:${fallbackReason}`
      : null;
  const fallingBack = fallbackKey !== null && pendingFallbackKey === fallbackKey;

  useEffect(() => {
    if (
      !fallbackKey ||
      fallbackHandledRef.current === fallbackKey ||
      !fallbackRoom ||
      !fileId ||
      !isSourceFallbackReason(fallbackReason) ||
      !fallbackRoom.members?.some((member) => member.is_self && member.connected) ||
      watchTogetherConnection.connectionState !== "connected"
    )
      return;
    fallbackHandledRef.current = fallbackKey;
    setPendingFallbackKey(fallbackKey);
    void playbackCapabilitiesV2(config)
      .then((capabilities) => {
        if (!capabilities.features.includes("watch_party_source_fallback_v1")) return null;
        return fallbackSource({
          selectionRevision: fallbackRoom.selection_revision,
          failedFileId: fileId,
          reason: fallbackReason,
        });
      })
      .catch(() => {
        // Keep the original playback refusal if there is no common fallback or
        // the request fails. A fresh playback attempt can try again.
      })
      .finally(() => {
        setPendingFallbackKey((current) => (current === fallbackKey ? null : current));
      });
  }, [
    config,
    fallbackKey,
    fallbackRoom,
    fallbackReason,
    fallbackSource,
    fileId,
    watchTogetherConnection.connectionState,
  ]);

  const initialSubtitleErrorKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (!session.initialSubtitleError || !session.playbackAttemptId) return;
    const key = `${session.playbackAttemptId}:${session.initialSubtitleError}`;
    if (initialSubtitleErrorKeyRef.current === key) return;
    initialSubtitleErrorKeyRef.current = key;
    toast.error(session.initialSubtitleErrorTitle ?? "That subtitle track can't be used", {
      description: session.initialSubtitleError,
    });
  }, [session.initialSubtitleError, session.initialSubtitleErrorTitle, session.playbackAttemptId]);

  const trackSelectionNotice = session.trackSelectionNotice;
  useEffect(() => {
    if (!trackSelectionNotice) return;
    toast.error(trackSelectionNotice.title, {
      id: `track-selection-${trackSelectionNotice.id}`,
      description: trackSelectionNotice.message,
    });
  }, [trackSelectionNotice]);

  const activeVersion = useMemo(() => {
    const resolved = resolveEffectiveVersion(playbackVersions, session);
    if (resolved) return resolved;
    // When the plan or a committed-source event names a concrete effective
    // virtual source, a list miss is a real miss: never fall through to an
    // unrelated release's row for the live session. An older plan with no
    // effective identity keeps the historical id/first-row fallback.
    if (session.effectiveVirtualUri) return undefined;
    return (
      (fileId ? playbackVersions.find((v) => v.file_id === fileId) : undefined) ??
      playbackVersions[0]
    );
  }, [fileId, playbackVersions, session]);

  // The plan's audio inventory is authoritative for the effective source after
  // a version fallback; item metadata can be stale. Fall back to the version's
  // probed tracks only when the plan publishes none (old plans, audiobooks).
  const audioTracks = useMemo(
    () =>
      (session.planAudioTracks?.length ?? 0) > 0
        ? (session.planAudioTracks ?? [])
        : (activeVersion?.audio_tracks ?? []),
    [activeVersion, session.planAudioTracks],
  );
  // Declared rows are the playing version's own inventory: show them now and
  // let the menu mark them unverified. Only an empty list waits on a probe.
  const audioMenuProvisional = isOverlayInventoryProvisional(
    session.audioInventoryProvisional,
    audioTracks.length,
  );

  const versionSubtitles = useMemo(() => {
    if (activeVersion?.subtitle_tracks !== undefined) {
      if (activeVersion.subtitle_tracks.length === 0) {
        return [];
      }
      // Shared ordinal derivation; see subtitleInventory.ts. Keeps this
      // fallback's indexes aligned with the plan's published inventory.
      return buildPublishedSubtitleTracks(activeVersion.subtitle_tracks);
    }
    return subtitles;
  }, [activeVersion, subtitles]);

  // Merge point for the two gated fallbacks above: the audio menu's
  // `audioTracks` and the subtitle menu's `versionSubtitles`. Both read catalog
  // metadata only when the plan publishes no inventory, and both must switch
  // back to plan evidence as soon as it lands — keep their gates aligned.
  const playableSubtitles = useMemo(
    () => resolvePlayableSubtitles(session.subtitleUrls, versionSubtitles),
    [session.subtitleUrls, versionSubtitles],
  );
  // The playing version's declared subtitle rows are shown immediately; only
  // an empty list waits for probe evidence. The active version is resolved
  // against the effective source above, so this is never a sibling's list.
  const subtitleMenuProvisional = isOverlayInventoryProvisional(
    session.subtitleInventoryProvisional,
    playableSubtitles.length,
  );
  // What the menu may actually offer. A provisional inventory with no selectable
  // rows of its own leaves only catalog fallback rows, whose ordinals belong to
  // another inventory; the menu stays in discovery instead of letting a click
  // address a slot this file may not carry.
  const subtitleMenuTracks = useMemo(
    () =>
      resolveSubtitleMenuTracks(session.subtitleUrls, versionSubtitles, subtitleMenuProvisional),
    [session.subtitleUrls, versionSubtitles, subtitleMenuProvisional],
  );

  const handleSwitchVersion = useCallback(
    (newFileId: number, currentPosition: number) => {
      session.switchVersion(newFileId, currentPosition);
    },
    [session],
  );

  const handleSelectAutoVersion = useCallback(() => {
    session.selectAutoVersion();
  }, [session]);

  /**
   * Manually re-lists the title's video candidates for the version menu.
   *
   * The re-list is asynchronous: the endpoint accepts the work and returns an
   * admin job, which is waited on to a terminal state before the server's
   * answer is read back. The caller's refresh control stays locked for the
   * whole flow. A rejected refresh throws before any state write, so the rows
   * already on screen stay put rather than disappearing behind a failed
   * request. A second press of the locked control cancels the job instead.
   */
  const { awaitAdminJob } = useRealtimeEvents();
  const handleRefreshVersions = useCallback(async () => {
    await awaitVirtualCandidatesRefresh(contentId, awaitAdminJob);
    // Re-read the watch detail so the version list and the indexer releases
    // both come from the server's answer rather than a client-side merge.
    const detail = await queryClient.fetchQuery({
      queryKey: itemKeys.watchDetail(contentId, fileId, libraryId),
      queryFn: () => fetchWatchDetail(contentId, fileId, libraryId),
      staleTime: 0,
    });
    // The refreshed list is the server's, but it carries no liveness verdict —
    // the parent checks those separately. Re-stamp the parent's verdicts before
    // the rows replace the list so the menu keeps its `available: false`
    // warnings rather than dropping them until another projection fires.
    const stamped = applyVersionAvailability(detail.versions, versionLiveness);
    // The refreshed list is the server's, but the live session may still be on
    // a source the re-list did not return (a rotation, or a candidate the
    // server resolved the collapsed row to). Re-key that committed source into
    // the fresh list so the version, audio and subtitle menus keep resolving
    // their active row — and the provenance they display — against it.
    const live = sessionRef.current;
    setPlaybackVersions(
      mergeResolvedLiveVersion(
        stamped,
        live.mediaFileId,
        { mediaFileId: live.mediaFileId, effectiveVirtualUri: live.effectiveVirtualUri },
        versions,
      ),
    );
    setIndexerReleaseRows(detail.indexer_releases ?? []);
  }, [awaitAdminJob, contentId, fileId, libraryId, queryClient, versionLiveness, versions]);
  const handleCancelRefresh = useCallback(async () => {
    await cancelVirtualCandidatesRefresh(contentId);
  }, [contentId]);

  const activePlaybackVersion = useMemo(
    () => playbackVersions.find((version) => version.file_id === session.mediaFileId),
    [playbackVersions, session.mediaFileId],
  );
  const trickplayAvailable = activePlaybackVersion?.trickplay_available === true;
  const trickplayQuery = useWatchTrickplay(
    contentId,
    session.mediaFileId ?? undefined,
    trickplayAvailable,
  );
  // A failed refresh retains query data. A terminal response withdraws the
  // cached previews; transient failures keep them until the query recovers.
  const trickplay =
    trickplayAvailable && (!trickplayQuery.isError || transientTrickplayError(trickplayQuery.error))
      ? (trickplayQuery.data ?? null)
      : null;
  const refetchTrickplay = trickplayQuery.refetch;
  const lastTrickplayRefresh = useRef<{ fileId: number | null; at: number } | null>(null);
  const trickplayRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    lastTrickplayRefresh.current = null;
    return () => {
      if (trickplayRefreshTimer.current !== null) clearTimeout(trickplayRefreshTimer.current);
      trickplayRefreshTimer.current = null;
    };
  }, [session.mediaFileId, trickplayAvailable]);
  useEffect(() => {
    // The query owns retry and polling delays after a manifest request fails.
    if (trickplayQuery.isError && trickplayRefreshTimer.current !== null) {
      clearTimeout(trickplayRefreshTimer.current);
      trickplayRefreshTimer.current = null;
    }
  }, [trickplayQuery.isError]);
  const handleTrickplayError = useCallback(() => {
    if (trickplayQuery.isError) return;
    const now = Date.now();
    const previous = lastTrickplayRefresh.current;
    const refresh = () => {
      trickplayRefreshTimer.current = null;
      lastTrickplayRefresh.current = { fileId: session.mediaFileId, at: Date.now() };
      void refetchTrickplay({ cancelRefetch: false });
    };
    if (previous?.fileId === session.mediaFileId && now - previous.at < 60_000) {
      if (trickplayRefreshTimer.current === null) {
        trickplayRefreshTimer.current = setTimeout(refresh, 60_000 - (now - previous.at));
      }
      return;
    }
    if (trickplayRefreshTimer.current !== null) clearTimeout(trickplayRefreshTimer.current);
    refresh();
  }, [refetchTrickplay, session.mediaFileId, trickplayQuery.isError]);

  // Re-key the live session file into the version list whenever it changes.
  // A start or switch can target a file the current list does not carry, which
  // leaves `activePlaybackVersion` undefined and makes `activeVersion` (and the
  // audio menu, duration and chapters that read through it) fall back to
  // another release's row.
  useEffect(() => {
    const liveFileId = session.mediaFileId;
    if (liveFileId == null) return;
    setPlaybackVersions((current) =>
      mergeResolvedLiveVersion(
        current,
        liveFileId,
        { mediaFileId: liveFileId, effectiveVirtualUri: session.effectiveVirtualUri },
        versions,
      ),
    );
  }, [session.effectiveVirtualUri, session.mediaFileId, versions]);

  // Identity of the current substitution. The dismissal below is scoped to it:
  // a later rotation to a different effective release re-shows the notice, and
  // an explicit user switch (which changes the effective identity, and usually
  // clears the substitution) moves the key too.
  const substitutionKey = `${session.plan?.requested_media_file_id ?? ""}:${session.mediaFileId ?? ""}:${session.effectiveVirtualUri ?? ""}`;
  useEffect(() => {
    setVersionSwapNoticeDismissed(false);
  }, [substitutionKey]);

  const handleEnded = useCallback(() => {
    onEnded?.({
      positionSeconds: session.durationSeconds ?? 0,
      durationSeconds: session.durationSeconds ?? undefined,
      lastFileId: session.mediaFileId,
      selectedFileId: session.plan?.requested_media_file_id ?? fileId ?? session.mediaFileId,
      lastResolution: activePlaybackVersion?.resolution,
      lastHDR: activePlaybackVersion?.hdr,
      lastCodecVideo: activePlaybackVersion?.codec_video,
      lastEditionKey: activePlaybackVersion?.edition_key,
    });
  }, [
    activePlaybackVersion,
    fileId,
    onEnded,
    session.durationSeconds,
    session.mediaFileId,
    session.plan?.requested_media_file_id,
  ]);

  // The committed request row changes when the viewer switches version in the
  // player. Persist it as soon as the server acknowledges the switch: post-roll
  // close and next-episode navigation do not wait for `onEnded`, so a viewer
  // who leaves through those controls would otherwise lose the switch.
  const committedRequestedFileId = session.plan?.requested_media_file_id ?? null;
  const lastRequestedFileIdRef = useRef(committedRequestedFileId);
  useEffect(() => {
    if (lastRequestedFileIdRef.current === committedRequestedFileId) return;
    lastRequestedFileIdRef.current = committedRequestedFileId;
    onRequestedVersionChange?.(committedRequestedFileId);
  }, [committedRequestedFileId, onRequestedVersionChange]);

  const handleSwitchAudio = useCallback(
    (index: number, currentPosition: number) => {
      session.switchAudioTrack(index, currentPosition);
    },
    [session],
  );

  const updatePlaybackState = session.updatePlaybackState;
  const handlePlaybackStateChange = useCallback(
    (state: PlayerPlaybackStateChange) => {
      playbackPositionRef.current = state.currentTime;
      updatePlaybackState(state.currentTime, state.playing);
      onPlaybackStateChange?.(state);
    },
    [onPlaybackStateChange, updatePlaybackState],
  );

  // Audio is complete once a virtual file has a real multi-track inventory (or
  // the file is local); subtitles once the plan publishes any inventory.
  const isVirtualActiveFile = activePlaybackVersion?.container === "virtual";

  const applyAudioInventory = session.applyAudioInventory;
  const refreshSubtitles = session.refreshSubtitles;
  const applyInventoryUpdate = session.applyInventoryUpdate;

  /**
   * Follows the effective version a transport just committed to. The realtime
   * event is the only signal for a serve-layer rotation, which never publishes
   * a new plan: adopt the new identity and declared audio inventory, and when
   * the source actually changed, re-read the subtitle inventory through the
   * existing refresh path so the subtitle menu follows too.
   */
  const applyCommittedSource = session.applyCommittedSource;
  const handleSourceCommitted = useCallback(
    (payload: PlaybackSourceCommittedPayload) => {
      // Compare against the live session identity (which a prior commit event
      // has already moved), not the plan, so a duplicate delivery is a no-op.
      const current = sessionRef.current;
      const identityChanged =
        (payload.effective_media_file_id != null &&
          payload.effective_media_file_id !== current.mediaFileId) ||
        (payload.effective_virtual_uri != null &&
          payload.effective_virtual_uri !== current.effectiveVirtualUri);
      applyCommittedSource(
        {
          effectiveMediaFileId: payload.effective_media_file_id ?? null,
          effectiveVirtualUri: payload.effective_virtual_uri ?? null,
          inventoryStatus: payload.inventory_status ?? null,
        },
        payload.audio_tracks ?? [],
      );
      // A moved identity needs the subtitle inventory re-read. So does a
      // verified push against a still-declared subtitle menu: the probe landed
      // under the same source, and without the replan the menu would keep its
      // provisional badge and declared list forever.
      const subtitlesDeclaredBehindVerified =
        payload.inventory_status === "verified" && current.subtitleInventoryProvisional;
      if (identityChanged || subtitlesDeclaredBehindVerified) {
        void refreshSubtitles(playbackPositionRef.current);
      }
    },
    [applyCommittedSource, refreshSubtitles],
  );

  // Dedicated deferred-inventory polling loop for plans that promised enumeration
  // on `inventory_url`: polls that endpoint directly with If-None-Match ETag.
  useEffect(() => {
    if (
      !session.sessionId ||
      !session.mediaFileId ||
      session.loading ||
      session.replacing ||
      !session.inventoryPending ||
      !session.inventoryUrl
    ) {
      return;
    }

    const sessionId = session.sessionId;
    const mediaFileId = session.mediaFileId;
    const inventoryUrl = session.inventoryUrl;
    let cancelled = false;
    let completedAttempts = 0;
    let scheduledAttempts = 0;
    let timer: number | null = null;
    const deadline = Date.now() + INVENTORY_REFRESH_DEADLINE_MS;

    const scheduleNextPoll = () => {
      if (cancelled) return;
      const current = sessionRef.current;
      // Mirror the legacy catalog poll's identity guard: a deferred round armed
      // for one session (or file) must not mark a replacement, and the deferred
      // round owns a different `inventory_url` when a fresh one re-arms. The live
      // ref is read here rather than trusted from the closure.
      if (
        current.sessionId !== sessionId ||
        current.mediaFileId !== mediaFileId ||
        !current.inventoryPending ||
        current.inventoryUrl !== inventoryUrl
      ) {
        return;
      }
      if (completedAttempts >= INVENTORY_REFRESH_MAX_ATTEMPTS || Date.now() >= deadline) {
        // The expected identity lets the shared guard drop a stale timer whose
        // round was replaced after this callback was armed but before it fired.
        sessionRef.current.markInventoryExhausted({ sessionId, mediaFileId });
        return;
      }
      const delay = inventoryPollDelayMs(scheduledAttempts, false);
      if (delay === 0) return;
      scheduledAttempts += 1;
      timer = window.setTimeout(() => void poll(), delay);
    };

    const poll = async () => {
      let done = false;
      try {
        done = await sessionRef.current.pollInventory();
      } catch {
        done = false;
      }
      if (cancelled) return;
      completedAttempts += 1;
      if (!done) scheduleNextPoll();
    };

    scheduleNextPoll();
    return () => {
      cancelled = true;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [
    session.inventoryPending,
    session.inventoryUrl,
    inventoryRetryNonce,
    session.loading,
    session.mediaFileId,
    session.replacing,
    session.sessionId,
  ]);

  // Legacy catalog watch-detail poll for older plans that do not carry inventory_url.
  useEffect(() => {
    if (
      !session.sessionId ||
      !session.mediaFileId ||
      session.loading ||
      session.replacing ||
      session.inventoryPending
    ) {
      return;
    }

    // A declared (or deferred/failed-probe) inventory is not the source's real
    // tracks, so keep polling for probe evidence even when the list looks full
    // or the file is not virtual. The virtual file's single synthesized entry
    // is the other reason to keep looking.
    const needsAudio =
      session.audioInventoryProvisional ||
      (isVirtualActiveFile && session.planAudioTracks.length <= 1);
    // Gate on what the menu can actually render, and on whether it is still
    // declared metadata, not on whether the plan published any entry: a
    // non-selectable placeholder must not suppress the poll, and a provisional
    // list must keep polling until a verified one clears the badge.
    // Gate on the plan's own inventory, not the rendered menu: an empty plan
    // now shows the watch-detail evidence as a fallback, and the bounded poll
    // is still the repair that mints real sidecar URLs and permissions.
    const needsSubtitles =
      !hasSelectableSessionSubtitles(session.subtitleUrls) || session.subtitleInventoryProvisional;
    if (!needsAudio && !needsSubtitles) return;

    // This poll is a fresh discovery round. If a previous round (the deferred
    // `inventory_url` poll, or an earlier legacy round) already spent its budget
    // and drew the terminal failure, hand the badge to this retry: the menus go
    // back to "Discovering…" for as long as the catalog fallback is actually
    // looking, and only its own exhausted budget re-marks the failure. Without
    // this, the terminal "discovery did not complete" notice would sit beside an
    // active retry.
    sessionRef.current.markInventoryRetrying();

    const mediaFileId = session.mediaFileId;
    const sessionId = session.sessionId;
    // A serve-layer rotation can move the effective virtual source without
    // changing the session or the collapsed file id, so the poll must key on
    // the source identity too. Otherwise a delayed response for source A is
    // applied as source B's inventory.
    const effectiveVirtualUri = session.effectiveVirtualUri;
    let cancelled = false;
    // Aborts the in-flight catalog read when the poll is torn down — unmount or
    // a superseding switch that restarts this effect. Without it a slow
    // response can land after teardown and populate the shared cache with the
    // outgoing file's inventory.
    const controller = new AbortController();
    let completedAttempts = 0;
    // Counts every scheduled attempt, successful or not, so the early cadence
    // advances even when requests fail and the completed-attempt cap does not.
    let scheduledAttempts = 0;
    let timer: number | null = null;
    let audioComplete = !needsAudio;
    let subtitlesComplete = !needsSubtitles;
    // Absolute wall-clock deadline so an error loop that never completes a
    // fetch cannot poll past the safety window.
    const deadline = Date.now() + INVENTORY_REFRESH_DEADLINE_MS;

    const scheduleNextPoll = () => {
      if (cancelled) return;
      // Mirror the deferred `inventory_url` poll's identity guard: a timer
      // armed for one session (or file) must not mark a replacement, and a
      // deferred round that re-armed while this timer was waiting owns the
      // inventory now. The live ref is read here rather than trusted from the
      // closure, so neither a replaced session nor a fresh deferred round is
      // marked by a stale legacy timer.
      const current = sessionRef.current;
      if (
        current.sessionId !== sessionId ||
        current.mediaFileId !== mediaFileId ||
        current.inventoryPending
      ) {
        return;
      }
      const delay = inventoryPollDelayMs(scheduledAttempts, audioComplete && subtitlesComplete);
      if (delay === 0) return;
      // The budget is spent. A catalog read that came back empty is not proof
      // the probe completed — an unprobed, slow, or failed probe also serves
      // empty tracks — so the client must not promote the declared inventory to
      // verified on its own. The badge and the server's declared state stay put
      // until an authoritative payload carries `inventory_status: "verified"`.
      if (completedAttempts >= INVENTORY_REFRESH_MAX_ATTEMPTS || Date.now() >= deadline) {
        // Same contract as the deferred `inventory_url` poll: an exhausted
        // budget on a still-declared menu is a terminal failure, not an
        // indefinite "Discovering…". `markInventoryExhausted` only sets the
        // failure while a menu is still provisional, so a verified list is
        // never mislabeled. The expected identity lets it drop a stale timer
        // from a replaced session.
        sessionRef.current.markInventoryExhausted({ sessionId, mediaFileId });
        return;
      }
      scheduledAttempts += 1;
      timer = window.setTimeout(() => void poll(), delay);
    };

    const poll = async () => {
      // The first attempt must read past the mounted query's stale window: a
      // payload fetched at page mount would otherwise come back from the cache
      // without a request, hiding the inventory the probe just persisted.
      const isFirstAttempt = scheduledAttempts === 1;
      try {
        // Key on the *live* session file, not the mount-time request: an
        // in-player version switch moves `session.mediaFileId` while the
        // `fileId` prop still names the version the page opened with. A stale
        // key would keep reading the mount version's inventory (or a cached
        // payload for it) and never see the newly selected version's probe.
        const detail = await queryClient.fetchQuery({
          queryKey: itemKeys.watchDetail(contentId, mediaFileId, libraryId),
          queryFn: () =>
            fetchWatchDetail(contentId, mediaFileId, libraryId, { signal: controller.signal }),
          staleTime: isFirstAttempt ? 0 : WATCH_DETAIL_STALE_TIME_MS,
        });
        if (cancelled) return;
        // Only completed responses count toward the cap; transient fetch
        // errors are retried without burning the attempt budget.
        completedAttempts += 1;
        const current = sessionRef.current;
        // A version switch or a serve-layer rotation can land while the
        // request is in flight. If the session no longer targets the
        // file/session/source we polled for, discard the response silently; the
        // restarted effect picks up the new target.
        if (
          current.mediaFileId !== mediaFileId ||
          current.sessionId !== sessionId ||
          current.effectiveVirtualUri !== effectiveVirtualUri
        ) {
          return;
        }
        // Probe metadata is persisted to the effective candidate row, not the
        // collapsed virtual row the session id names, so resolve the target the
        // same way the menus do: effective virtual URI first, collapsed id as
        // the fallback for ordinary files and older plans.
        const version = resolveEffectiveVersion(detail.versions, {
          mediaFileId,
          effectiveVirtualUri,
        });
        if (version) {
          const nextAudioTracks = version.audio_tracks ?? [];
          // The menu may replace the plan's inventory when the poll resolved a
          // different file than the plan names (a virtual candidate vs. the
          // collapsed row). `applyAudioInventory` owns the replacement, but the
          // poll decides here whether the attempt found anything: a same-file
          // list no richer than the plan's keeps the poll running so a later
          // probe can still expand it.
          const audioTargetChanged = version.file_id !== current.mediaFileId;
          // A serve-layer rotation moves the resolved candidate while the
          // collapsed id stays put, so the version identity has to follow the
          // same source the inventory does. Only a virtual source carries a
          // candidate URI; an ordinary version switch moves by id alone and
          // must leave `effectiveVirtualUri` untouched.
          const resolvedVirtualUri = isVirtualActiveFile ? version.file_path : undefined;
          // A changed candidate URI under the same resolved row is an
          // authoritative identity update even when the catalog list is the
          // same length; it must not be held to the enrichment guard below.
          const candidateUriChanged =
            resolvedVirtualUri !== undefined && resolvedVirtualUri !== current.effectiveVirtualUri;
          // A declared (provisional) list is upgraded by the catalog's probed
          // list even when that list is shorter; an already-verified list still
          // only accepts a strict superset so a poorer row cannot shrink it.
          if (
            nextAudioTracks.length > 0 &&
            (audioTargetChanged ||
              candidateUriChanged ||
              current.audioInventoryProvisional ||
              nextAudioTracks.length > current.planAudioTracks.length)
          ) {
            if (resolvedVirtualUri !== undefined) {
              applyAudioInventory(nextAudioTracks, version.file_id, resolvedVirtualUri);
            } else {
              applyAudioInventory(nextAudioTracks, version.file_id);
            }
            audioComplete = true;
          } else if (candidateUriChanged && resolvedVirtualUri !== undefined) {
            // Same resolved row, unchanged (possibly empty) list, but the
            // candidate moved. Re-key the identity without pretending the probe
            // landed, and keep polling for the new source's inventory.
            applyAudioInventory(nextAudioTracks, version.file_id, resolvedVirtualUri);
          }
          const resolvedSubtitleTracks = version.subtitle_tracks ?? [];
          // The first-play probe persists its tracks to the resolved candidate
          // row, which a plan that has not learned `effective_virtual_uri` yet
          // cannot name: the resolved version is then the collapsed row and
          // carries nothing. Keep the current-row lookup first so a row with its
          // own probe inventory wins, then fall back to the first row that
          // actually carries probed tracks. The candidate fallback is scoped
          // under that lookup, never instead of it.
          const currentRowSubtitleTracks =
            detail.versions.find((candidate) => candidate.file_id === mediaFileId)
              ?.subtitle_tracks ?? [];
          const nextSubtitleTracks =
            resolvedSubtitleTracks.length > 0
              ? resolvedSubtitleTracks
              : isVirtualActiveFile
                ? currentRowSubtitleTracks.length > 0
                  ? currentRowSubtitleTracks
                  : (detail.versions.find(
                      (candidate) => (candidate.subtitle_tracks?.length ?? 0) > 0,
                    )?.subtitle_tracks ?? [])
                : resolvedSubtitleTracks;
          const subtitleMenuIncomplete =
            !hasSelectableSessionSubtitles(current.subtitleUrls) ||
            current.subtitleInventoryProvisional;
          if (subtitleMenuIncomplete && nextSubtitleTracks.length > 0) {
            // The plan's inventory is empty or still declared; a no-op
            // track_change replan re-reads it (URLs included) without changing
            // the A/V transport, so the stream keeps playing. Only a plan that
            // stops reporting a provisional inventory ends the retry budget: a
            // replan that lands still-declared evidence leaves the poll running
            // for the probe that clears the badge, and a transient replan
            // failure does not spend the budget either.
            const filled = await refreshSubtitles(playbackPositionRef.current);
            if (cancelled) return;
            // A failed replan keeps the budget; a successful one ends it only
            // once the adopted plan is no longer declared.
            if (filled && !sessionRef.current.subtitleInventoryProvisional) {
              subtitlesComplete = true;
            }
          }
        }
      } catch {
        // Best effort; a later attempt may still succeed.
      }
      scheduleNextPoll();
    };

    scheduleNextPoll();

    return () => {
      cancelled = true;
      controller.abort();
      if (timer !== null) window.clearTimeout(timer);
    };
    // The track counts that gate the poll are read once when it starts. They
    // are deliberately not dependencies: filling the inventory in must not
    // restart the attempt budget. The provisional flags are different — a
    // declared push after a verified one re-marks the menu, and the poll has to
    // come back to clear it again, so a transition restarts (or single-shots)
    // the poll.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- restart the poll on the inventory-identity transitions listed below.
  }, [
    applyAudioInventory,
    applyInventoryUpdate,
    contentId,
    inventoryRetryNonce,
    isVirtualActiveFile,
    libraryId,
    queryClient,
    refreshSubtitles,
    session.audioInventoryProvisional,
    session.effectiveVirtualUri,
    session.inventoryPending,
    session.loading,
    session.mediaFileId,
    session.replacing,
    session.sessionId,
    session.subtitleInventoryProvisional,
  ]);

  /**
   * Persists an in-player subtitle choice for the whole series.
   *
   * buildSubtitleChoiceRequests decides what a pick is worth storing and
   * where; this only issues the requests. They are independent on purpose: a
   * failed settings write must not cost the user the track they picked, and a
   * failed track write must not cost them the language, so each is best effort
   * on its own rather than one composite request that half-applies.
   */
  const handleSubtitleChanged = useCallback(
    (index: number | null, inventoryTrack?: SubtitleInventoryItemV3) => {
      const requests = buildSubtitleChoiceRequests({
        seriesId: seriesContext?.seriesId ?? contentId,
        index,
        tracks: playableSubtitles,
        inventoryTrack,
      });
      for (const request of requests) {
        void sendSubtitleChoiceRequest(config, request).catch(() => {
          // Best effort.
        });
      }
    },
    [config, seriesContext, contentId, playableSubtitles],
  );

  useEffect(() => {
    chapterRefreshAttemptsRef.current.clear();
  }, [contentId, playbackRequestKey]);

  useEffect(() => {
    if (watchTogetherConnection.replacementReason) return;
    const room = watchTogetherConnection.room;
    if (!watchTogetherRoomId || !watchTogetherRoomToken || !room) {
      handledSelectionRevisionRef.current = null;
      return;
    }

    const sameSelection =
      room.selected_content_id === contentId &&
      room.selected_file_id === fileId &&
      room.selected_library_id === libraryId;
    if (sameSelection) {
      handledSelectionRevisionRef.current = room.selection_revision;
      return;
    }
    if (room.phase !== "playing" || !room.selected_content_id) {
      return;
    }
    if (handledSelectionRevisionRef.current === room.selection_revision) {
      return;
    }

    handledSelectionRevisionRef.current = room.selection_revision;
    // The room changed its selection; this viewer did not press Play.
    playbackController.startPlayback(
      {
        contentId: room.selected_content_id,
        fileId: room.selected_file_id,
        libraryId: room.selected_library_id,
        roomId: watchTogetherRoomId,
        roomToken: watchTogetherRoomToken,
        restart: true,
      },
      "automatic",
    );
  }, [
    contentId,
    fileId,
    libraryId,
    playbackController,
    watchTogetherConnection.room,
    watchTogetherConnection.replacementReason,
    watchTogetherRoomId,
    watchTogetherRoomToken,
  ]);

  useEffect(() => {
    if (!session.sessionId || !session.mediaFileId || session.loading || session.replacing) {
      return;
    }

    const activeVersion = playbackVersions.find(
      (version) => version.file_id === session.mediaFileId,
    );
    if (!activeVersion || (activeVersion.chapters?.length ?? 0) > 0) {
      return;
    }

    if (chapterRefreshAttemptsRef.current.has(session.mediaFileId)) {
      return;
    }
    chapterRefreshAttemptsRef.current.add(session.mediaFileId);

    // Force the read past the mounted query's stale window. A fresh cached
    // payload that still lacks chapters would otherwise be returned without a
    // network request, spending this file's single repair attempt for nothing.
    void queryClient.fetchQuery({
      queryKey: itemKeys.watchDetail(contentId, fileId, libraryId),
      queryFn: () => fetchWatchDetail(contentId, fileId, libraryId),
      staleTime: 0,
    });
  }, [
    contentId,
    fileId,
    libraryId,
    queryClient,
    session.loading,
    session.mediaFileId,
    session.replacing,
    session.sessionId,
    playbackVersions,
  ]);

  const handledRealtimeReconnectRef = useRef(0);
  // A reconcile belongs to one content and reconnect generation. Keep the
  // latest parent inputs in refs so a versions or liveness change landing while
  // the read is in flight updates what the answer is stamped with instead of
  // canceling the effect and discarding the reconcile.
  const versionsRef = useRef(versions);
  versionsRef.current = versions;
  const versionLivenessRef = useRef(versionLiveness);
  versionLivenessRef.current = versionLiveness;
  const reconcileGeneration = `${contentId}\u0000${fileId ?? ""}\u0000${libraryId ?? ""}\u0000${realtimeReconnectEpoch}`;
  const reconcileGenerationRef = useRef(reconcileGeneration);
  reconcileGenerationRef.current = reconcileGeneration;
  useEffect(() => {
    if (
      realtimeConnectionState !== "connected" ||
      realtimeReconnectEpoch === 0 ||
      handledRealtimeReconnectRef.current === realtimeReconnectEpoch ||
      !session.sessionId ||
      !session.mediaFileId ||
      session.loading ||
      session.replacing
    ) {
      return;
    }
    // Claim the reconnect before the request so a versions or liveness identity
    // change while it is in flight cannot start a second one.
    handledRealtimeReconnectRef.current = realtimeReconnectEpoch;

    const generation = reconcileGeneration;
    // Same key as the mounted `useWatchDetail` query, but always read fresh: a
    // cached payload can predate a server-side inventory change. The fresh rows
    // carry no liveness, so the verdicts the parent stamped on its own copy are
    // re-applied below before this list replaces them.
    void queryClient
      .fetchQuery({
        queryKey: itemKeys.watchDetail(contentId, fileId, libraryId),
        queryFn: () => fetchWatchDetail(contentId, fileId, libraryId),
        staleTime: 0,
      })
      .then((detail) => {
        // Only a newer content target or reconnect generation obsoletes this
        // answer. Session, versions, and liveness movement while the read was
        // in flight must not lose the reconcile for the generation that claimed
        // it.
        if (reconcileGenerationRef.current !== generation) return;

        // The fresh server rows carry no liveness verdict; re-stamp the
        // parent's current checked verdicts onto them before replacing the list
        // so the menu keeps its `available: false` warnings rather than
        // dropping them until another projection fires.
        const stamped = applyVersionAvailability(detail.versions, versionLivenessRef.current);
        // A serve-layer rotation during the disconnect can move the effective
        // source without changing the collapsed file id. Re-key the committed
        // live source into the fresh list the same way a manual refresh does,
        // so `activeVersion` follows the rotation instead of falling through
        // to the first row.
        const live = sessionRef.current;
        setPlaybackVersions(
          mergeResolvedLiveVersion(
            stamped,
            live.mediaFileId,
            { mediaFileId: live.mediaFileId, effectiveVirtualUri: live.effectiveVirtualUri },
            versionsRef.current,
          ),
        );
      })
      .catch(() => {
        // Reconcile again on the next connection; keep the current markers meanwhile.
      });
  }, [
    contentId,
    fileId,
    libraryId,
    queryClient,
    realtimeConnectionState,
    realtimeReconnectEpoch,
    reconcileGeneration,
    session.loading,
    session.mediaFileId,
    session.replacing,
    session.sessionId,
  ]);

  const handleRealtimeEvent = useCallback(
    (event: PlaybackRealtimeEventEnvelope) => {
      if (event.name === "marker_thumbnail_ready") {
        const image = event.payload;
        if (image.file_id !== session.mediaFileId || image.session_id !== session.sessionId) return;
        setPlaybackVersions((current) =>
          current.map((version) =>
            version.file_id !== image.file_id
              ? version
              : {
                  ...version,
                  marker_segments: version.marker_segments?.map((marker) =>
                    marker.kind === image.kind &&
                    marker.start_seconds === image.start_seconds &&
                    marker.end_seconds === image.end_seconds
                      ? {
                          ...marker,
                          thumbnail_url: image.thumbnail_url,
                          thumbnail_thumbhash: image.thumbnail_thumbhash,
                          thumbnail_capture_seconds: image.thumbnail_capture_seconds,
                        }
                      : marker,
                  ),
                },
          ),
        );
        return;
      }
      if (event.name === "chapter_thumbnail_ready") {
        const { file_id, chapter_index, thumbnail_url, thumbnail_thumbhash } = event.payload;
        if (file_id !== session.mediaFileId) {
          return;
        }

        setPlaybackVersions((current) =>
          patchChapterThumbnail(
            current,
            file_id,
            chapter_index,
            thumbnail_url,
            thumbnail_thumbhash,
          ),
        );
        return;
      }

      if (event.name !== "markers_updated") {
        return;
      }

      const {
        file_id,
        intro: nextIntro,
        credits: nextCredits,
        recap: nextRecap,
        preview: nextPreview,
        marker_segments: nextSegments,
      } = event.payload;
      if (file_id !== session.mediaFileId) {
        return;
      }

      setPlaybackVersions((current) =>
        patchVersionMarkers(
          current,
          file_id,
          nextIntro,
          nextCredits,
          nextRecap,
          nextPreview,
          nextSegments,
        ),
      );
    },
    [session.mediaFileId, session.sessionId],
  );

  // The server tells us whether a terminal is worth retrying. A retryable
  // virtual-source refusal gets a Try again action, as does a retryable
  // transcode start failure (e.g. a seek anchor that failed on a transient
  // relay error — a fresh start re-lists and can succeed where the same
  // plan could not proceed); every other terminal keeps the plain Go Back
  // dead-end. An adaptation refusal always offers the action: the server
  // marks it non-retryable because replaying the same plan cannot work, but
  // a *fresh start* can re-list and re-resolve the source.
  const canRetryTerminal =
    !session.plan &&
    (session.errorReason === "virtual_source_unavailable" ||
    session.errorReason === "transcode_start_failed"
      ? session.errorRetryable === true
      : isAdaptationUnavailableReason(session.errorReason));
  // A session answered gone cannot be replanned or reconnected. The only
  // recovery is a new start at the position the viewer reached.
  const canRestartSession = !session.plan && session.connectionStatus === "gone";
  const retryInFlight = session.retrying;

  // The plan is the player's contract: without one there is no transport, no
  // timeline and no track inventory to render against.
  if (!session.plan || !session.streamUrl || !session.sessionId) {
    if (session.loading || fallingBack) {
      return (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black">
          <div className="flex flex-col items-center gap-3">
            <div className="h-8 w-8 animate-spin rounded-full border-2 border-white/20 border-t-white" />
            <span className="text-sm text-white/60">
              {fallingBack ? "Finding a compatible version for everyone..." : "Loading player..."}
            </span>
          </div>
        </div>
      );
    }

    return (
      <div className="bg-background fixed inset-0 z-50 flex items-center justify-center px-6">
        <div className="surface-panel-subtle flex max-w-md flex-col items-center gap-4 rounded-[1.8rem] px-8 py-8 text-center">
          <div className="space-y-2">
            <p className="text-base font-semibold text-white">
              {session.errorTitle ?? session.connectionErrorTitle ?? "Playback unavailable"}
            </p>
            <p className="text-sm text-white/60">
              {session.error ?? session.connectionError ?? "Vio could not start playback."}
            </p>
          </div>
          <div className="flex flex-col items-center gap-2">
            {canRestartSession ? (
              <button
                onClick={() => {
                  session.restartPlayback();
                }}
                type="button"
                className="rounded-[0.95rem] bg-white px-4 py-2 text-sm font-semibold text-black transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60"
              >
                Restart playback
              </button>
            ) : canRetryTerminal ? (
              <button
                onClick={() => {
                  session.retryStart();
                }}
                type="button"
                disabled={retryInFlight}
                className="rounded-[0.95rem] bg-white px-4 py-2 text-sm font-semibold text-black transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60"
              >
                Try again
              </button>
            ) : null}
            <button
              onClick={() => {
                void onExit();
              }}
              type="button"
              className="rounded-[0.95rem] bg-white/10 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-white/20"
            >
              Go Back
            </button>
          </div>
        </div>
      </div>
    );
  }

  // A version switch keeps the old stream playing while the replacement plan
  // is resolved (for virtual versions the server round trip can take seconds).
  // Surface that with a small non-blocking chip near the controls instead of
  // replacing the whole page — the viewer keeps watching the old stream.
  const switchingIndicator = session.replacing ? (
    <div
      role="status"
      aria-label="Switching version"
      className="pointer-events-none absolute top-[max(4.5rem,calc(env(safe-area-inset-top)+3.5rem))] left-1/2 z-50 -translate-x-1/2"
    >
      <div className="flex items-center gap-2 rounded-full border border-white/15 bg-black/70 px-3 py-1.5 text-xs font-medium text-white/80 shadow-lg backdrop-blur">
        <span className="h-3 w-3 animate-spin rounded-full border-2 border-white/25 border-t-white" />
        Switching version…
      </div>
    </div>
  ) : null;

  // The server may substitute a different version (e.g. HDR→SDR) when the
  // requested one is not playable on this device, and a serve-layer rotation can
  // move the release mid-stream without publishing a new plan. Either way the
  // client owes the viewer an honest, non-blocking notice saying what is playing
  // instead of what they asked for.
  //
  // The requested row is the plan's requested id (a serve-layer rotation never
  // moves it). The effective release is the LIVE session identity first — it
  // moves on a rotation — then the plan's. A virtual requested row defeats the
  // id comparison: the server collapses `effective_media_file_id` onto the
  // requested id and publishes the concrete candidate as `effective_virtual_uri`
  // instead, so the candidate's path is compared against the requested row's
  // own path. When the requested row carries no path (older responses) the
  // comparison says nothing, so the notice stays quiet rather than guess.
  //
  // The server also publishes the substitution additively
  // (`substituted_from_file_id` + `substitution_reason`), which is authoritative
  // when present and lets the notice name the cause without re-deriving it.
  const plan = session.plan;
  const requestedFileId = plan?.requested_media_file_id ?? fileId ?? null;
  const requestedVersion =
    requestedFileId != null
      ? playbackVersions.find((v) => v.file_id === requestedFileId)
      : undefined;
  // The live session moves the effective id on a rotation; otherwise the plan's
  // effective id is the authority (the session id can lag, or equal the
  // collapsed requested row). So the session id wins only once it has actually
  // moved off the requested row.
  const planEffectiveFileId = plan?.effective_media_file_id ?? null;
  const sessionMediaFileId = session.mediaFileId;
  const effectiveFileId =
    sessionMediaFileId != null && sessionMediaFileId !== requestedFileId
      ? sessionMediaFileId
      : (planEffectiveFileId ?? sessionMediaFileId);
  const effectiveVirtualUri = session.effectiveVirtualUri ?? plan?.effective_virtual_uri ?? null;
  const virtualSubstitution =
    !!effectiveVirtualUri &&
    requestedVersion?.file_path !== undefined &&
    requestedVersion.file_path !== effectiveVirtualUri;
  const versionWasSubstituted =
    (plan?.substituted_from_file_id != null && plan.substituted_from_file_id !== effectiveFileId) ||
    (requestedFileId != null && effectiveFileId != null && requestedFileId !== effectiveFileId) ||
    virtualSubstitution;
  // Name the row the plan actually landed on when we can resolve it, so the
  // notice says what is playing instead of only that something changed. The
  // effective row is resolved through the effective identity, not the
  // session's requested id, because the effective id is the authority.
  const effectiveVersionRow = resolveEffectiveVersion(playbackVersions, {
    // A published virtual URI is the sole identity of the effective candidate;
    // the collapsed id names the neutral row, so falling back to it would label
    // the wrong row. Only fall back to the id for ordinary files and older
    // plans that publish no URI.
    mediaFileId: effectiveVirtualUri ? null : effectiveFileId,
    effectiveVirtualUri,
  });
  const effectiveVersionLabel = effectiveVersionRow
    ? buildEffectiveVersionLabel(effectiveVersionRow)
    : null;
  const reasonPhrase = substitutionReasonPhrase(plan?.substitution_reason);
  const substitutionCopy = reasonPhrase
    ? effectiveVersionLabel
      ? `${reasonPhrase}, so Vio is playing ${effectiveVersionLabel} instead.`
      : `${reasonPhrase} — playing a different version.`
    : effectiveVersionLabel
      ? `The selected version wasn't available, so Vio is playing ${effectiveVersionLabel} instead.`
      : "Playing a different version than selected — the requested version isn't playable on this device.";
  const versionSwapNotice =
    versionWasSubstituted && !versionSwapNoticeDismissed ? (
      <div className="absolute top-[max(4.5rem,calc(env(safe-area-inset-top)+3.5rem))] left-1/2 z-50 -translate-x-1/2">
        <div className="flex items-center gap-2 rounded-full border border-white/15 bg-black/70 px-3 py-1.5 text-xs font-medium text-white/80 shadow-lg backdrop-blur">
          <span>{substitutionCopy}</span>
          <button
            type="button"
            aria-label="Dismiss version notice"
            onClick={() => setVersionSwapNoticeDismissed(true)}
            className="cursor-pointer rounded-full px-1 text-white/60 transition-colors hover:text-white"
          >
            ✕
          </button>
        </div>
      </div>
    ) : null;

  // Find the duration of the selected file so the player knows the total
  // length even when the stream is chunked (no Content-Length header).
  const selectedDuration =
    session.durationSeconds ??
    playbackVersions.find((v) => v.file_id === session.mediaFileId)?.duration ??
    playbackVersions[0]?.duration;
  const selectedVersion = activeVersion;
  const activeChapters =
    (playbackVersions.find((v) => v.file_id === session.mediaFileId) ?? selectedVersion)
      ?.chapters ?? [];
  const activeMarkers = resolveActiveVersionMarkers(selectedVersion);

  return (
    <>
      {switchingIndicator}
      {versionSwapNotice}
      <VideoPlayer
        contentId={contentId}
        title={title}
        year={year}
        streamUrl={session.streamUrl}
        plan={session.plan}
        planRevision={session.planRevision}
        transportRevision={session.transportRevision}
        replayRevision={session.replayRevision}
        browserEngine={session.browserEngine}
        nativeOriginalSupport={session.nativeOriginalSupport}
        shouldAutoPlay={session.shouldAutoPlay}
        replanning={session.replanning}
        replanningQuality={session.replanningQuality}
        replacing={session.replacing}
        pendingSwitchFileId={session.pendingSwitchFileId}
        replanError={fallingBack ? null : session.error}
        replanErrorTitle={session.errorTitle}
        initialSubtitleError={session.initialSubtitleError}
        sessionId={session.sessionId}
        selectedVersion={selectedVersion}
        versions={playbackVersions}
        indexerReleases={indexerReleaseRows}
        virtualRanking={virtualRanking}
        activeFileId={session.mediaFileId}
        activeVirtualUri={session.effectiveVirtualUri}
        chapters={activeChapters}
        trickplay={trickplay}
        trickplayUpdatedAt={trickplayQuery.dataUpdatedAt}
        onTrickplayError={handleTrickplayError}
        onSwitchVersion={watchTogetherRoomId ? undefined : handleSwitchVersion}
        onSelectAutoVersion={watchTogetherRoomId ? undefined : handleSelectAutoVersion}
        autoFallback={session.autoFallback}
        onRefreshVersions={handleRefreshVersions}
        onCancelRefresh={handleCancelRefresh}
        subtitleUrls={subtitleMenuTracks}
        initialPosition={session.initialPosition}
        onQualitySelect={session.changeQuality}
        onSubtitleTrackChange={session.changeSubtitleTrack}
        onPlanFailure={session.recoverFromFailure}
        onConnectionLost={session.recoverConnection}
        onRetryConnection={session.retryConnection}
        onSessionLost={session.reportSessionLost}
        onSessionGone={session.markSessionGone}
        onRestartPlayback={session.restartPlayback}
        onExpediteReconnect={session.expediteReconnect}
        connectionStatus={session.connectionStatus}
        connectionErrorTitle={session.connectionErrorTitle}
        connectionError={session.connectionError}
        onPlanInvalidated={session.invalidatePlan}
        onReanchorSeek={session.reanchorSeek}
        onApplySubtitleTrack={session.applySubtitleTrack}
        preferredSubtitleLanguage={preferredSubtitleLanguage}
        preferredSubtitleTrackSignature={preferredSubtitleTrackSignature}
        subtitleMode={session.initialSubtitleError ? "off" : subtitleMode}
        showForcedSubtitles={session.initialSubtitleError ? false : showForcedSubtitles}
        profileLanguage={profileLanguage}
        intro={activeMarkers.intro}
        introSkipMode={introSkipMode}
        credits={activeMarkers.credits}
        recap={activeMarkers.recap}
        autoSkipRecap={autoSkipRecap}
        preview={activeMarkers.preview}
        markerSegments={selectedVersion?.marker_segments}
        autoPlayNextPreview={autoPlayNextPreview}
        canEditMarkers={canEditMarkers}
        onMarkersEdited={(fileId, markers) =>
          setPlaybackVersions((current) =>
            patchVersionMarkers(
              current,
              fileId,
              markers.intro,
              markers.credits,
              markers.recap,
              markers.preview,
            ),
          )
        }
        duration={selectedDuration}
        // The session's preference, not the caller's: the server normalizes what
        // was requested and the menu has to light up whatever it settled on.
        qualityPreference={session.qualityPreference}
        seriesContext={seriesContext}
        onNavigateEpisode={onNavigateEpisode}
        displayMode={displayMode}
        onPictureInPictureChange={onPictureInPictureChange}
        autoEnterPictureInPicture={autoEnterPictureInPicture}
        onPlaybackStateChange={handlePlaybackStateChange}
        onPlaybackTransportReady={onPlaybackTransportReady}
        onFirstFrame={session.reportFirstFrame}
        seekIntervals={seekIntervals}
        onRealtimeEvent={handleRealtimeEvent}
        onRealtimeConnectionStateChange={setRealtimeConnectionState}
        onExit={onExit}
        onMinimize={onMinimize}
        onEnded={handleEnded}
        onRefreshSubtitles={session.refreshSubtitles}
        onSourceCommitted={handleSourceCommitted}
        onInventoryUpdated={session.applyInventoryUpdate}
        audioTracks={audioTracks}
        activeAudioIndex={session.audioTrackIndex}
        onAudioSelect={handleSwitchAudio}
        audioInventoryProvisional={audioMenuProvisional}
        subtitleInventoryProvisional={subtitleMenuProvisional}
        inventoryPending={session.inventoryPending}
        inventoryFailed={session.inventoryFailed}
        onRetryInventory={handleRetryInventory}
        onSubtitleChanged={handleSubtitleChanged}
        onReturnFromPostRoll={onReturnFromPostRoll}
        watchTogetherRoomId={watchTogetherRoomId}
        watchTogetherConnection={watchTogetherConnection}
      />
    </>
  );
}
