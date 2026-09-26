import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import {
  SERIES_SUBTITLE_SETTING_KEYS,
  seriesSubtitleSettingIdentity,
} from "@/lib/seriesSubtitleSettings";
import type { PrePlaySubtitleSelection } from "@/player/types";
import { derivePersistedSubtitleMode } from "@/player/utils/subtitleMode";
import type {
  DownloadedSubtitle,
  SubtitleDownloadRequest,
  SubtitleLanguageDetection,
  SubtitleSearchRequest,
  SubtitleSearchResponse,
  SubtitleUploadRequest,
} from "@/api/types";

import { itemKeys, settingsKeys, subtitleKeys } from "./keys";
import { isSettingValueMissing } from "./settingValues";
import { canonicalLanguageWireValue } from "@/lib/languageNames";

interface DownloadSubtitleResponse {
  subtitle: DownloadedSubtitle;
}

export async function fetchDownloadedSubtitles(
  mediaFileId: number,
  options?: RequestInit,
): Promise<DownloadedSubtitle[]> {
  const response = await v2("GET /api/v2/subtitles/{media_file_id}", {
    path: { media_file_id: String(mediaFileId) },
    signal: options?.signal ?? undefined,
  });
  // The existing track selector still consumes numeric database IDs. Reject
  // unrepresentable IDs until that remaining player model moves to strings.
  return response.subtitles.map((subtitle) => {
    const id = Number(subtitle.id);
    const fileId = Number(subtitle.media_file_id);
    if (!Number.isSafeInteger(id) || id <= 0 || !Number.isSafeInteger(fileId) || fileId <= 0) {
      throw new Error("Unsupported subtitle identifier");
    }
    return { ...subtitle, id, media_file_id: fileId };
  });
}

export async function searchSubtitles(
  request: SubtitleSearchRequest,
  options?: RequestInit,
): Promise<SubtitleSearchResponse> {
  const languages = request.languages
    .map(canonicalLanguageWireValue)
    .filter((v): v is string => Boolean(v));
  return v2("POST /api/v2/subtitles/search", {
    body: { media_file_id: String(request.media_file_id), languages },
    signal: options?.signal ?? undefined,
  });
}

export async function downloadSubtitle(
  request: SubtitleDownloadRequest,
  options?: RequestInit,
): Promise<DownloadSubtitleResponse> {
  const snapshot = captureProfileRequestContext();
  if (!snapshot) throw new StaleApiRequestContextError();
  const response = await v2("POST /api/v2/subtitles/download", {
    body: {
      media_file_id: String(request.media_file_id),
      provider: request.provider,
      subtitle_id: request.subtitle_id,
      language: canonicalLanguageWireValue(request.language) ?? request.language,
      release_name: request.release_name,
      score: request.score,
      hearing_impaired: request.hearing_impaired,
    },
    signal: options?.signal ?? undefined,
    profileContext: snapshot,
    retryAuthentication: false,
  });
  if (!isCapturedProfileAuthorityActive(snapshot)) throw new StaleApiRequestContextError();
  const id = Number(response.subtitle.id);
  const fileId = Number(response.subtitle.media_file_id);
  if (
    !Number.isSafeInteger(id) ||
    id <= 0 ||
    String(id) !== response.subtitle.id ||
    fileId !== request.media_file_id ||
    String(fileId) !== response.subtitle.media_file_id
  ) {
    throw new Error("Unsupported subtitle identifier");
  }
  return { subtitle: { ...response.subtitle, id, media_file_id: fileId } };
}

export async function uploadSubtitle(
  request: SubtitleUploadRequest,
  options?: RequestInit,
): Promise<DownloadSubtitleResponse> {
  const snapshot = captureProfileRequestContext();
  if (!snapshot) throw new StaleApiRequestContextError();
  const response = await v2("POST /api/v2/subtitles/upload", {
    form: {
      media_file_id: String(request.media_file_id),
      file: request.file,
      language: request.language,
      language_override: request.language_override ? "true" : "false",
      release_name: request.release_name,
      hearing_impaired: request.hearing_impaired ? "true" : "false",
    },
    signal: options?.signal ?? undefined,
    profileContext: snapshot,
    retryAuthentication: false,
  });
  if (!isCapturedProfileAuthorityActive(snapshot)) throw new StaleApiRequestContextError();
  const id = Number(response.subtitle.id),
    fileId = Number(response.subtitle.media_file_id);
  if (
    !Number.isSafeInteger(id) ||
    id <= 0 ||
    String(id) !== response.subtitle.id ||
    fileId !== request.media_file_id ||
    String(fileId) !== response.subtitle.media_file_id
  )
    throw new Error("Unsupported subtitle identifier");
  return { subtitle: { ...response.subtitle, id, media_file_id: fileId } };
}

export async function detectSubtitleLanguage(
  file: File,
  language?: string,
  options?: RequestInit,
): Promise<SubtitleLanguageDetection> {
  const snapshot = captureProfileRequestContext();
  if (!snapshot) throw new StaleApiRequestContextError();
  const result = await v2("POST /api/v2/subtitles/detect-language", {
    form: { file, language },
    signal: options?.signal ?? undefined,
    profileContext: snapshot,
  });
  if (!isCapturedProfileAuthorityActive(snapshot)) throw new StaleApiRequestContextError();
  return result;
}

export function useDownloadedSubtitles(mediaFileId: number | undefined) {
  return useQuery({
    queryKey: mediaFileId != null ? subtitleKeys.downloaded(mediaFileId) : subtitleKeys.all,
    queryFn: () => fetchDownloadedSubtitles(mediaFileId!),
    enabled: mediaFileId != null,
  });
}

/**
 * Whether this server can search online subtitle providers. Callers should hide
 * online search only when the server explicitly answers `enabled: false`; an
 * unresolved or failed probe (e.g. an older server) fails open.
 */
export function useSubtitleProviderStatus() {
  return useQuery({
    queryKey: subtitleKeys.providerStatus(),
    queryFn: () => v2("GET /api/v2/subtitles/providers/status"),
  });
}

// Subtitle preferences feed the effective defaults on item details; a
// series-keyed preference feeds every episode's detail, so invalidate broadly.
function invalidateItemDetails(queryClient: ReturnType<typeof useQueryClient>): Promise<unknown> {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: itemKeys.details() }),
    queryClient.invalidateQueries({ queryKey: ["catalog", "items"] }),
  ]);
}

/**
 * Clears the persisted subtitle override (saved when a track is manually
 * selected during playback) so profile-level auto selection applies again.
 * Keyed by the movie's content ID or the episode's series ID.
 *
 * A manual selection writes two stores: the specialized per-series row holding
 * the concrete track, and the canonical language/mode settings at
 * profile_series. Resetting has to clear both. profile_series is the first
 * scope in the manifest's resolution order for those keys, so leaving the
 * canonical rows behind would keep resolving the abandoned language for every
 * episode of the series with nothing in the UI able to remove it.
 */
export function useDeleteSubtitlePreference() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (prefId: string) => {
      await v2("DELETE /api/v2/subtitle-prefs/{series_id}", { path: { series_id: prefId } });
      await Promise.all(
        SERIES_SUBTITLE_SETTING_KEYS.map((key) =>
          v2("DELETE /api/v2/settings/values/{key}", {
            path: { key },
            query: seriesSubtitleSettingIdentity(prefId),
          }).catch((error: unknown) => {
            // Nothing stored at this scope is the state a reset asks for.
            if (isSettingValueMissing(error)) return;
            throw error;
          }),
        ),
      );
    },
    onSuccess: () =>
      Promise.all([
        invalidateItemDetails(queryClient),
        // The player and the settings screens read these keys through the
        // effective endpoint, so the cleared rows have to leave that cache too.
        queryClient.invalidateQueries({ queryKey: [...settingsKeys.all, "values"] }),
      ]),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to reset subtitle preference");
    },
  });
}

interface SetSubtitlePreferenceInput {
  /** Movie content ID or episode series ID (preferences are series-scoped). */
  prefId: string;
  /** The chosen track, or null to persist "subtitles off". */
  selection: PrePlaySubtitleSelection | null;
  /** Preserve the effective forced-subtitle behavior in the replaced row. */
  showForcedSubtitles?: boolean;
}

/**
 * Persists a pre-play subtitle choice as the item's override — the same
 * "always play this track" (or "off") preference a manual in-player
 * selection saves — so the choice sticks across visits and sessions.
 */
export function useSetSubtitlePreference() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ prefId, selection, showForcedSubtitles }: SetSubtitlePreferenceInput) =>
      v2("PUT /api/v2/subtitle-prefs/{series_id}", {
        path: { series_id: prefId },
        body: {
          subtitle_language: selection?.language ?? "",
          // -1 is the "no track" sentinel the contract admits for "off".
          subtitle_track_index: selection?.track_index ?? -1,
          subtitle_mode: derivePersistedSubtitleMode(
            selection ? (selection.track_index ?? -1) : null,
          ),
          // The contract spells "no signature" as an absent member, not null.
          track_signature: selection
            ? {
                source: selection.source,
                language: selection.language,
                codec: selection.codec,
                label: selection.label,
                forced: selection.forced,
                hearing_impaired: selection.hearing_impaired,
              }
            : undefined,
          show_forced_subtitles: showForcedSubtitles,
        },
      }),
    onSuccess: () => invalidateItemDetails(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save subtitle preference");
    },
  });
}

export function useDownloadSubtitle() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: SubtitleDownloadRequest) => downloadSubtitle(request),
    retry: false,
    networkMode: "always",
    onSuccess: async (_response, request) => {
      toast.success("Subtitle downloaded");
      await queryClient.invalidateQueries({
        queryKey: subtitleKeys.downloaded(request.media_file_id),
      });
    },
    onError: (err) => {
      if (!(err instanceof StaleApiRequestContextError))
        toast.error(err instanceof Error ? err.message : "Failed to download subtitle");
    },
  });
}

export function useUploadSubtitle() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (request: SubtitleUploadRequest) => uploadSubtitle(request),
    retry: false,
    networkMode: "always",
    onSuccess: async (_response, request) => {
      toast.success("Subtitle uploaded");
      await queryClient.invalidateQueries({
        queryKey: subtitleKeys.downloaded(request.media_file_id),
      });
    },
    onError: (err) => {
      if (!(err instanceof StaleApiRequestContextError))
        toast.error(err instanceof Error ? err.message : "Failed to upload subtitle");
    },
  });
}
