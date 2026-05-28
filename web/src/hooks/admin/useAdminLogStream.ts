import { startTransition, useDeferredValue, useEffect, useMemo, useState } from "react";
import { getAccessToken } from "@/api/client";
import type {
  AdminLogAppendMessage,
  AdminLogErrorMessage,
  AdminLogSnapshotMessage,
  AdminLogStream,
  AdminLogStreamMessage,
  AuditLogEntry,
  OperationalLogEntry,
} from "@/api/types";
import type { AdminLogQuery } from "@/hooks/queries/admin/logs";

type StreamEntryMap = {
  app: OperationalLogEntry;
  audit: AuditLogEntry;
};

type ConnectionState = "connecting" | "live" | "disconnected";

const APPEND_FLUSH_MS = 75;

export interface AdminLogStreamResult<TEntry> {
  rows: TEntry[];
  nextCursor?: string;
  isConnecting: boolean;
  isLive: boolean;
  error?: string;
  connectionState: ConnectionState;
  /** Force a fresh connection attempt (the stream does not auto-retry on drop). */
  reconnect: () => void;
}

export function buildAdminLogStreamQuery(params: AdminLogQuery) {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === "") continue;
    search.set(key, String(value));
  }
  return search.toString();
}

export function buildAdminLogStreamUrl(
  stream: AdminLogStream,
  params: AdminLogQuery,
  token: string | null,
  location: Pick<Location, "protocol" | "host">,
) {
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const search = new URLSearchParams();
  search.set("stream", stream);

  const query = buildAdminLogStreamQuery(params);
  if (query) {
    for (const [key, value] of new URLSearchParams(query).entries()) {
      search.set(key, value);
    }
  }
  if (token) {
    search.set("token", token);
  }

  return `${protocol}//${location.host}/api/v1/admin/logs/ws?${search.toString()}`;
}

export function applyAdminLogAppend<T extends { id: number }>(rows: T[], entry: T, limit: number) {
  const next = [entry, ...rows.filter((row) => row.id !== entry.id)];
  return next.slice(0, limit);
}

export function applyAdminLogAppends<T extends { id: number }>(
  rows: T[],
  entries: T[],
  limit: number,
) {
  return entries.reduce((current, entry) => applyAdminLogAppend(current, entry, limit), rows);
}

export function useAdminLogStream<TStream extends AdminLogStream>(
  stream: TStream,
  params: AdminLogQuery,
  enabled: boolean,
): AdminLogStreamResult<StreamEntryMap[TStream]> {
  const [rows, setRows] = useState<StreamEntryMap[TStream][]>([]);
  const [nextCursor, setNextCursor] = useState<string>();
  const [connectionState, setConnectionState] = useState<ConnectionState>("disconnected");
  const [error, setError] = useState<string>();
  const [reconnectNonce, setReconnectNonce] = useState(0);

  const deferredParams = useDeferredValue(params);
  const queryString = useMemo(() => buildAdminLogStreamQuery(deferredParams), [deferredParams]);
  const limit = deferredParams.limit ?? 100;

  useEffect(() => {
    if (!enabled) {
      setConnectionState("disconnected");
      setError(undefined);
      return;
    }

    let ws: WebSocket | null = null;
    let appendQueue: StreamEntryMap[TStream][] = [];
    let flushTimer: number | null = null;

    const clearFlushTimer = () => {
      if (flushTimer !== null) {
        window.clearTimeout(flushTimer);
        flushTimer = null;
      }
    };

    const flushAppends = () => {
      flushTimer = null;
      if (appendQueue.length === 0) {
        return;
      }

      const pending = appendQueue;
      appendQueue = [];
      startTransition(() => {
        setRows((current) => applyAdminLogAppends(current, pending, limit));
      });
    };

    const scheduleFlush = () => {
      if (flushTimer !== null) {
        return;
      }
      flushTimer = window.setTimeout(flushAppends, APPEND_FLUSH_MS);
    };

    // Debounce the initial attempt; an explicit reconnect() retries immediately.
    const connectDelay = reconnectNonce === 0 ? 250 : 0;
    const connectTimer = window.setTimeout(() => {
      const url = buildAdminLogStreamUrl(stream, deferredParams, getAccessToken(), window.location);
      try {
        ws = new WebSocket(url);
      } catch {
        setConnectionState("disconnected");
        setError("Unable to open log stream.");
        return;
      }

      setConnectionState("connecting");
      setError(undefined);

      ws.onopen = () => {
        setConnectionState("live");
      };

      ws.onmessage = (event) => {
        const message = parseAdminLogStreamMessage(event.data);
        if (!message) {
          return;
        }

        if (message.type === "snapshot") {
          appendQueue = [];
          clearFlushTimer();
          startTransition(() => {
            setRows(message.entries as StreamEntryMap[TStream][]);
            setNextCursor(message.next_cursor);
            setError(undefined);
          });
          return;
        }

        if (message.type === "append") {
          appendQueue.push(message.entry as StreamEntryMap[TStream]);
          scheduleFlush();
          return;
        }

        setError(message.message);
      };

      ws.onerror = () => {
        setConnectionState("disconnected");
        setError("Log stream disconnected.");
      };

      ws.onclose = () => {
        setConnectionState("disconnected");
      };
    }, connectDelay);

    return () => {
      window.clearTimeout(connectTimer);
      clearFlushTimer();
      if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
        ws.close();
      }
    };
  }, [stream, queryString, limit, enabled, deferredParams, reconnectNonce]);

  return {
    rows,
    nextCursor,
    isConnecting: connectionState === "connecting",
    isLive: connectionState === "live",
    error,
    connectionState,
    reconnect: () => setReconnectNonce((n) => n + 1),
  };
}

function parseAdminLogStreamMessage(value: unknown): AdminLogStreamMessage | null {
  if (typeof value !== "string") {
    return null;
  }

  try {
    const parsed = JSON.parse(value) as
      | AdminLogSnapshotMessage
      | AdminLogAppendMessage
      | AdminLogErrorMessage;
    if (!parsed || typeof parsed.type !== "string") {
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}
