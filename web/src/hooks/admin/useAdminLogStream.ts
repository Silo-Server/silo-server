import { startTransition, useDeferredValue, useEffect, useMemo, useState } from "react";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import {
  adminLogsSocketProtocols,
  buildAdminLogsSocketQuery,
  buildAdminLogsSocketUrl,
  mintAdminLogsSocketTicket,
} from "@/api/v2/adminLogsSocket";
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
  const queryString = useMemo(() => buildAdminLogsSocketQuery(deferredParams), [deferredParams]);
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
    // Authority is captured once per connection attempt: the credential is
    // minted under it, and frames arriving after it changed are discarded.
    let closedByEffect = false;
    const connectDelay = reconnectNonce === 0 ? 250 : 0;
    const connectTimer = window.setTimeout(() => {
      const authority = captureProfileRequestContext();
      if (!authority) {
        setConnectionState("disconnected");
        setError("Select an administrator profile to stream logs.");
        return;
      }
      const authorityActive = () => isCapturedProfileAuthorityActive(authority);
      setConnectionState("connecting");
      setError(undefined);
      void mintAdminLogsSocketTicket(authority)
        .then((ticket) => {
          if (closedByEffect || !authorityActive()) return;
          const url = buildAdminLogsSocketUrl(stream, deferredParams, window.location);
          try {
            ws = new WebSocket(url, adminLogsSocketProtocols(ticket.ticket));
          } catch {
            setConnectionState("disconnected");
            setError("Unable to open log stream.");
            return;
          }
          const socket = ws;
          socket.onopen = () => {
            if (closedByEffect || !authorityActive()) {
              socket.close();
              return;
            }
            setConnectionState("live");
          };
          socket.onmessage = (event) => {
            if (closedByEffect || !authorityActive()) return;
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
          socket.onerror = () => {
            if (closedByEffect) return;
            setConnectionState("disconnected");
            setError("Log stream disconnected.");
          };
          socket.onclose = () => {
            if (closedByEffect) return;
            setConnectionState("disconnected");
          };
        })
        .catch(() => {
          if (closedByEffect || !authorityActive()) return;
          setConnectionState("disconnected");
          setError("Unable to open log stream.");
        });
    }, connectDelay);

    return () => {
      closedByEffect = true;
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
