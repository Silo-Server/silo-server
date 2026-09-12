import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  CreateHistoryImportRunRequest,
  EmbyConnectLoginRequest,
  EmbyConnectLoginResponse,
  PlexCheckResponse,
  PlexPinResponse,
} from "@/api/types";
import { v2, V2ProblemError, type V2Body } from "@/api/v2/request";
import { historyImportKeys } from "./keys";
import { toast } from "sonner";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";

export {
  historyImportRunFromV2,
  historyImportSourceFromV2,
  type PersonalImportRun,
} from "@/api/v2/historyImportTypes";
import { historyImportRunFromV2, historyImportSourceFromV2 } from "@/api/v2/historyImportTypes";
import type { PersonalImportRun } from "@/api/v2/historyImportTypes";
function importContext() {
  const context = captureProfileRequestContext();
  if (!context) throw new StaleApiRequestContextError();
  return context;
}
function checkImportContext(context: ProfileRequestContextSnapshot) {
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
}
function importScope() {
  const c = captureProfileRequestContext();
  return c ? `${c.serverOrigin}:${c.authContextVersion}:${c.profileId}` : "unavailable";
}
function runHeaders() {
  let location: string | undefined;
  let retryAfterMs = 5000;
  return {
    onResponse: (r: Response) => {
      location = r.headers.get("Location") ?? undefined;
      const seconds = Number(r.headers.get("Retry-After"));
      if (Number.isFinite(seconds) && seconds > 0) retryAfterMs = seconds * 1000;
    },
    metadata: () => ({ location, retryAfterMs }),
  };
}
function runLocation(id: string) {
  return `/api/v2/history-imports/runs/${encodeURIComponent(id)}`;
}

const STALE_TIME = 15_000;

function createRunBodyToV2(
  body: CreateHistoryImportRunRequest,
): V2Body<"POST /api/v2/history-imports/runs"> {
  const { source_id, ...rest } = body;
  return {
    ...rest,
    source: body.source as "emby" | "jellyfin" | "plex",
    ...(source_id !== undefined ? { source_id: String(source_id) } : {}),
  };
}

export function useHistoryImportSources() {
  return useQuery({
    queryKey: historyImportKeys.sources(),
    queryFn: () =>
      v2("GET /api/v2/history-imports/sources").then((d) => d.items.map(historyImportSourceFromV2)),
    staleTime: STALE_TIME,
  });
}

export function useHistoryImportRuns(limit = 10) {
  const scope = importScope();
  return useQuery({
    queryKey: [...historyImportKeys.runs(limit), scope],
    queryFn: async () => {
      if (scope !== importScope()) throw new StaleApiRequestContextError();
      const profileContext = importContext();
      const result = await v2("GET /api/v2/history-imports/runs", {
        query: { limit },
        profileContext,
      });
      checkImportContext(profileContext);
      return result.items.map(historyImportRunFromV2);
    },
    retry: false,
    staleTime: 5_000,
  });
}

export function useHistoryImportRun(id?: string) {
  const client = useQueryClient();
  const scope = importScope();
  const queryKey = [...historyImportKeys.run(id), scope];
  return useQuery({
    queryKey,
    queryFn: async () => {
      if (scope !== importScope()) throw new StaleApiRequestContextError();
      const profileContext = importContext();
      const previous = client.getQueryData<PersonalImportRun>(queryKey);
      if (previous?.location && previous.location !== runLocation(id!))
        throw new Error("Unexpected import status location. Refresh imports before trying again.");
      const h = runHeaders();
      const run = await v2("GET /api/v2/history-imports/runs/{id}", {
        path: { id: id! },
        profileContext,
        onResponse: h.onResponse,
      });
      checkImportContext(profileContext);
      const metadata = h.metadata();
      if (metadata.location && metadata.location !== runLocation(id!))
        throw new Error("Unexpected import status location. Refresh imports before trying again.");
      return { ...historyImportRunFromV2(run), ...metadata, location: runLocation(id!) };
    },
    enabled: !!id,
    retry: false,
    refetchInterval: (query) =>
      query.state.error || query.state.data?.terminal
        ? false
        : (query.state.data?.retryAfterMs ?? 5000),
  });
}

export function useLoginEmbyConnect() {
  return useMutation({
    mutationFn: (body: EmbyConnectLoginRequest): Promise<EmbyConnectLoginResponse> =>
      v2("POST /api/v2/history-imports/emby-connect/login", { body }),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to sign in with Emby Connect");
    },
  });
}

export function useCreatePlexPin() {
  return useMutation({
    mutationFn: (): Promise<PlexPinResponse> => v2("POST /api/v2/history-imports/plex/auth/pin"),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to start Plex sign-in");
    },
  });
}

export function useCheckPlexPin(sessionId?: string) {
  return useQuery({
    queryKey: historyImportKeys.plexCheck(sessionId),
    queryFn: (): Promise<PlexCheckResponse> =>
      v2("POST /api/v2/history-imports/plex/auth/check", { body: { session_id: sessionId! } }),
    enabled: !!sessionId,
    retry: false,
    refetchInterval: (query) => {
      const data = query.state.data;
      if (query.state.error) return false;
      if (!data) return 2_000;
      return data.authenticated ? false : 2_000;
    },
  });
}

export function useCreateHistoryImportRun() {
  const queryClient = useQueryClient();
  const scope = importScope();
  return useMutation({
    retry: false,
    mutationFn: async (body: CreateHistoryImportRunRequest) => {
      if (scope !== importScope()) throw new StaleApiRequestContextError();
      const profileContext = importContext();
      const h = runHeaders();
      const run = await v2("POST /api/v2/history-imports/runs", {
        body: createRunBodyToV2(body),
        profileContext,
        onResponse: h.onResponse,
      }).catch((error: unknown) => {
        if (error instanceof V2ProblemError || error instanceof StaleApiRequestContextError)
          throw error;
        throw new Error(
          "Import acceptance could not be confirmed. Refresh imports before submitting again.",
        );
      });
      checkImportContext(profileContext);
      const metadata = h.metadata();
      if (metadata.location !== runLocation(run.id))
        throw new Error(
          "Import acceptance could not be confirmed. Refresh imports before submitting again.",
        );
      return { ...historyImportRunFromV2(run), ...metadata };
    },
    onSuccess: (run) => {
      if (scope !== importScope()) return;
      queryClient.setQueryData([...historyImportKeys.run(run.id), scope], run);
      toast.success("Import queued");
      queryClient.invalidateQueries({ queryKey: historyImportKeys.runs() });
    },
    onError: (err) => {
      queryClient.invalidateQueries({ queryKey: historyImportKeys.runs() });
      toast.error(err instanceof Error ? err.message : "Failed to start import");
    },
  });
}
