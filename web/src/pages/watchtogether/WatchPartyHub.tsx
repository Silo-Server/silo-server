import { captureRoomCreationDraft, type RoomCreationDraft } from "@/api/v2/watchTogetherCreate";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { ClipboardPaste, Crown, Loader2, Sparkles, Ticket, UsersRound, Vote } from "lucide-react";
import { V2ProblemError, V2TransportError } from "@/api/v2/request";
import {
  ApiClientError,
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useOptionalAuth } from "@/hooks/useAuth";
import {
  createWatchTogetherRoom,
  getWatchTogetherRoom,
  joinWatchTogetherRoom,
  type WatchTogetherSelectionMode,
} from "@/lib/watchTogether";
import {
  forgetRecentRoom,
  markRecentRoomEnded,
  recentRoomKey,
  rememberRecentRoom,
  useRecentRooms,
  type RecentRoom,
} from "./hooks/useRecentRooms";

function describeJoinError(error: unknown) {
  if (error instanceof ApiClientError) {
    if (error.status === 404) return "Room not found.";
    if (error.status === 410 || error.status === 409) return "That room is no longer active.";
    return error.message;
  }
  return error instanceof Error ? error.message : "Failed to join room.";
}

function roomHref(roomId: string, token: string) {
  return `/rooms/${encodeURIComponent(roomId)}?room_token=${encodeURIComponent(token)}`;
}

const modeOptions = [
  { value: "host_pick", title: "Host picks", caption: "You choose, everyone watches." },
  { value: "vote", title: "Everyone votes", caption: "Anyone suggests, the room votes." },
] as const satisfies ReadonlyArray<{
  value: WatchTogetherSelectionMode;
  title: string;
  caption: string;
}>;

const howItWorks = [
  { title: "Open a room", caption: "Create one here, or start from any movie or episode page." },
  {
    title: "Share the code",
    caption: "Send the invite link or the eight-letter code to whoever is watching.",
  },
  {
    title: "Watch in sync",
    caption: "Play, pause, and seek together. The room ends two minutes after the host drops off.",
  },
] as const;

type RecentStatus = "checking" | "live" | "unknown" | "ended" | "gone";

/** HTTP status of a failed room read, from either client error shape. */
function roomReadStatus(error: unknown): number | null {
  if (
    error instanceof ApiClientError ||
    error instanceof V2ProblemError ||
    error instanceof V2TransportError
  )
    return error.status;
  return null;
}

/**
 * The Watch Party hub: join with a code, rejoin a recent party, or open an
 * empty room. Invite links (`?token=`) auto-join and never show the form
 * unless they fail. Starting a party from a title lives on the title's page.
 */
export default function WatchPartyHub() {
  useDocumentTitle("Watch Party");
  const navigate = useViewTransitionNavigate();
  const [searchParams] = useSearchParams();
  const token = searchParams.get("token")?.trim() ?? "";
  const hasInviteToken = token !== "";
  const auth = useOptionalAuth();
  const userId = auth?.user?.id ?? null;
  const profileId = auth?.profile?.id ?? null;

  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);

  // ── Join ────────────────────────────────────────────────────────────
  const [pendingJoin, setPendingJoin] = useState<{
    run: number;
    token: string;
    authority: NonNullable<ReturnType<typeof captureProfileRequestContext>>;
  } | null>(null);
  const joining =
    pendingJoin !== null &&
    pendingJoin.token === token &&
    isCapturedProfileAuthorityActive(pendingJoin.authority);
  const joinRun = useRef(0);
  const invalidateJoin = useCallback(() => {
    joinRun.current++;
  }, []);
  useLayoutEffect(() => {
    invalidateJoin();
    return invalidateJoin;
  }, [token, invalidateJoin]);

  const enterRoom = useCallback(
    (
      response: Awaited<ReturnType<typeof joinWatchTogetherRoom>>,
      authority: NonNullable<ReturnType<typeof captureProfileRequestContext>>,
    ) => {
      if (!response.room_access_token)
        throw new Error("Room access token was missing from the response.");
      if (userId !== null && profileId !== null) {
        rememberRecentRoom({
          room: response.room,
          token: response.room_access_token,
          userId,
          profileId,
        });
      }
      void authority;
      navigate(roomHref(response.room.room_id, response.room_access_token), { replace: true });
    },
    [navigate, profileId, userId],
  );

  const joinRoom = useCallback(
    async (input: { code?: string; join_token?: string }) => {
      const joinAuthority = captureProfileRequestContext();
      const run = ++joinRun.current;
      const active = () =>
        run === joinRun.current &&
        !!joinAuthority &&
        isCapturedProfileAuthorityActive(joinAuthority);
      if (!joinAuthority || !active()) return;
      setPendingJoin({ run, token, authority: joinAuthority });
      setError(null);
      try {
        const response = await joinWatchTogetherRoom({ ...input }, joinAuthority);
        if (!active()) return;
        enterRoom(response, joinAuthority);
      } catch (joinError) {
        if (!active() || joinError instanceof StaleApiRequestContextError) return;
        setError(describeJoinError(joinError));
      } finally {
        setPendingJoin((current) => (current?.run === run ? null : current));
      }
    },
    [enterRoom, token],
  );

  useEffect(() => {
    if (!hasInviteToken) return;
    void joinRoom({ join_token: token });
  }, [hasInviteToken, joinRoom, token]);

  const pasteCode = useCallback(async () => {
    try {
      const text = (await navigator.clipboard?.readText?.()) ?? "";
      const cleaned = text
        .replace(/[^a-z0-9]/gi, "")
        .toUpperCase()
        .slice(0, 12);
      if (cleaned) {
        setCode(cleaned);
        setError(null);
      }
    } catch {
      // Clipboard access refused: the user can still type the code.
    }
  }, []);

  // ── Open an empty room ──────────────────────────────────────────────
  const [selectionMode, setSelectionMode] = useState<WatchTogetherSelectionMode>("host_pick");
  const creationDraft = useRef<RoomCreationDraft | null>(null);
  const creationRun = useRef(0);
  const [pendingCreation, setPendingCreation] = useState<{
    run: number;
    draft: RoomCreationDraft;
  } | null>(null);
  // Derived, not stored: a replaced mode or authority releases the button so
  // a fresh draft can be sent while the stale one is still in flight.
  const creating =
    pendingCreation !== null &&
    pendingCreation.run === creationRun.current &&
    pendingCreation.draft.body.selection_mode === selectionMode &&
    !!pendingCreation.draft.authority &&
    isCapturedProfileAuthorityActive(pendingCreation.draft.authority);
  const invalidateCreation = useCallback(() => {
    creationRun.current++;
  }, []);
  useLayoutEffect(() => {
    creationDraft.current = null;
    invalidateCreation();
    return invalidateCreation;
  }, [selectionMode, invalidateCreation]);

  const createRoom = useCallback(async () => {
    let draft = creationDraft.current;
    if (!draft?.authority || !isCapturedProfileAuthorityActive(draft.authority)) {
      draft = captureRoomCreationDraft(selectionMode);
      creationDraft.current = draft;
    }
    if (!draft.authority || !isCapturedProfileAuthorityActive(draft.authority)) return;
    const run = ++creationRun.current;
    const active = () =>
      run === creationRun.current &&
      creationDraft.current === draft &&
      !!draft.authority &&
      isCapturedProfileAuthorityActive(draft.authority);
    setPendingCreation({ run, draft });
    setError(null);
    try {
      const response = await createWatchTogetherRoom(draft);
      if (!active()) return;
      enterRoom(response, draft.authority);
    } catch (createError) {
      if (!active() || createError instanceof StaleApiRequestContextError) return;
      setError(createError instanceof Error ? createError.message : "Failed to create room.");
    } finally {
      setPendingCreation((current) => (current?.run === run ? null : current));
    }
  }, [enterRoom, selectionMode]);

  // ── Recent parties ──────────────────────────────────────────────────
  const recent = useRecentRooms();
  const [statuses, setStatuses] = useState<Record<string, RecentStatus>>({});
  // Each room is verified once per mount. The effect reruns whenever the
  // recent list is re-read (its hook republishes on mount and on every
  // storage change), so the "already asked" memory lives in a ref and a
  // rerun must never cancel a read that is still in flight: the answer is
  // what moves the row off "Checking…".
  const checkedRef = useRef(new Set<string>());
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  useEffect(() => {
    const authority = captureProfileRequestContext();
    if (!authority) return;
    const active = () => mountedRef.current && isCapturedProfileAuthorityActive(authority);
    for (const entry of recent) {
      if (entry.user_id !== userId || entry.profile_id !== authority.profileId) continue;
      const key = recentRoomKey(entry);
      if (entry.ended || checkedRef.current.has(key)) continue;
      checkedRef.current.add(key);
      setStatuses((current) => ({ ...current, [key]: "checking" }));
      void getWatchTogetherRoom(entry.room_id, entry.token, authority)
        .then((response) => {
          if (!active()) return;
          const live = response.room.phase !== "ended";
          setStatuses((current) => ({ ...current, [key]: live ? "live" : "ended" }));
          if (!live) markRecentRoomEnded(entry);
        })
        .catch((readError: unknown) => {
          if (!active()) return;
          const status = roomReadStatus(readError);
          if (status === 403) {
            forgetRecentRoom(entry);
            setStatuses((current) => ({ ...current, [key]: "gone" }));
            return;
          }
          if (status === 404 || status === 409 || status === 410) {
            markRecentRoomEnded(entry);
            setStatuses((current) => ({ ...current, [key]: "ended" }));
            return;
          }
          setStatuses((current) => ({ ...current, [key]: "unknown" }));
        });
    }
  }, [recent, userId]);

  const visibleRecent = useMemo(
    () => recent.filter((entry) => statuses[recentRoomKey(entry)] !== "gone"),
    [recent, statuses],
  );

  const busy = joining || creating;
  const autoJoinPending = hasInviteToken && joining && !error;

  if (autoJoinPending) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col items-center gap-4 px-6 py-24 text-center">
        <Loader2 aria-hidden="true" className="text-muted-foreground size-8 animate-spin" />
        <h1 className="text-2xl font-semibold tracking-tight sm:text-3xl">Joining Watch Party</h1>
        <p className="text-muted-foreground max-w-sm text-sm" role="status">
          Taking you into the room.
        </p>
      </div>
    );
  }

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-12 px-6 py-12 sm:px-8">
      <header className="flex flex-col gap-3">
        <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
          Watch Party
        </p>
        <h1 className="max-w-2xl text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
          Watch together, wherever everyone is.
        </h1>
        <p className="text-muted-foreground max-w-xl text-base leading-7">
          Open a room, share the code, and everyone plays in sync. The host picks what's on, or the
          whole room votes.
        </p>
      </header>

      {error ? (
        <div
          role="alert"
          className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-200"
        >
          {error}
        </div>
      ) : null}

      <div className="grid gap-4 md:grid-cols-[1.15fr_1fr]">
        <section
          aria-labelledby="hub-start"
          className="surface-panel relative flex flex-col gap-5 overflow-hidden rounded-2xl p-6"
        >
          <div
            aria-hidden="true"
            className="from-primary/20 pointer-events-none absolute -top-24 -right-24 size-64 rounded-full bg-gradient-to-br to-transparent blur-3xl"
          />
          <div className="relative">
            <div className="flex items-center gap-2">
              <span className="bg-primary/15 text-primary flex size-8 items-center justify-center rounded-lg">
                <Sparkles className="size-4" />
              </span>
              <h2 id="hub-start" className="text-lg font-semibold">
                Start a party
              </h2>
            </div>
            <p className="text-muted-foreground mt-2 text-sm leading-6">
              Open an empty room now and pick what to watch once people arrive.
            </p>
          </div>
          <div
            role="radiogroup"
            aria-label="How the room picks"
            className="relative grid gap-2 sm:grid-cols-2"
          >
            {modeOptions.map((option) => {
              const selected = selectionMode === option.value;
              const Icon = option.value === "vote" ? Vote : Crown;
              return (
                <button
                  key={option.value}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  onClick={() => setSelectionMode(option.value)}
                  className={`flex items-start gap-3 rounded-xl border px-3 py-3 text-left text-sm transition-colors ${
                    selected
                      ? "border-foreground/50 bg-accent"
                      : "border-border/60 text-muted-foreground hover:bg-muted/60"
                  }`}
                >
                  <Icon className={`mt-0.5 size-4 shrink-0 ${selected ? "" : "opacity-60"}`} />
                  <span className="min-w-0">
                    <span className="text-foreground block font-medium">{option.title}</span>
                    <span className="text-muted-foreground mt-0.5 block text-xs">
                      {option.caption}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
          <div className="relative mt-auto flex flex-col gap-3">
            <Button
              type="button"
              onClick={() => void createRoom()}
              disabled={busy}
              className="h-12 rounded-lg text-base"
            >
              {creating ? "Creating..." : "Create Watch Party"}
            </Button>
            <p className="text-muted-foreground text-xs leading-5">
              Already know what you want to watch? Open its page and choose{" "}
              <span className="text-foreground">Start a party with this</span> from the menu.
            </p>
          </div>
        </section>

        <section
          aria-labelledby="hub-join"
          className="surface-panel-subtle flex flex-col gap-5 rounded-2xl p-6"
        >
          <div>
            <div className="flex items-center gap-2">
              <span className="bg-surface-raised text-muted-foreground flex size-8 items-center justify-center rounded-lg">
                <Ticket className="size-4" />
              </span>
              <h2 id="hub-join" className="text-lg font-semibold">
                Join a party
              </h2>
            </div>
            <p className="text-muted-foreground mt-2 text-sm leading-6">
              Got a code from the host? Enter it here. Invite links open straight into the room.
            </p>
          </div>
          <form
            className="flex flex-col gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              void joinRoom({ code: code.trim().toUpperCase() });
            }}
          >
            <label htmlFor="watch-room-code" className="sr-only">
              Room code
            </label>
            <div className="flex gap-2">
              <Input
                id="watch-room-code"
                value={code}
                onChange={(event) => {
                  setCode(event.target.value.toUpperCase());
                  if (error) setError(null);
                }}
                maxLength={12}
                autoCapitalize="characters"
                autoCorrect="off"
                spellCheck={false}
                placeholder="ABCD1234"
                disabled={busy}
                className="h-12 font-mono text-lg tracking-[0.3em] uppercase"
              />
              <Button
                type="button"
                variant="outline"
                onClick={() => void pasteCode()}
                disabled={busy}
                className="h-12 gap-1.5"
                aria-label="Paste room code"
              >
                <ClipboardPaste className="size-4" />
                <span className="hidden sm:inline">Paste</span>
              </Button>
            </div>
            <Button
              type="submit"
              variant="outline"
              disabled={busy || code.trim() === ""}
              className="h-12 rounded-lg"
            >
              {joining ? "Joining..." : "Join Watch Party"}
            </Button>
          </form>
          {hasInviteToken ? (
            <Button
              type="button"
              variant="ghost"
              onClick={() => void joinRoom({ join_token: token })}
              disabled={busy}
              className="self-start"
            >
              Retry Invite Link
            </Button>
          ) : null}
        </section>
      </div>

      {visibleRecent.length > 0 ? (
        <section className="flex flex-col gap-3">
          <h2 className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
            Your recent parties
          </h2>
          <ul className="flex flex-col gap-2">
            {visibleRecent.map((entry) => (
              <RecentRoomRow
                key={recentRoomKey(entry)}
                entry={entry}
                status={statuses[recentRoomKey(entry)] ?? (entry.ended ? "ended" : "checking")}
                onRejoin={() => navigate(roomHref(entry.room_id, entry.token))}
              />
            ))}
          </ul>
        </section>
      ) : null}

      <section aria-label="How it works" className="border-border/60 border-t pt-8">
        <ol className="grid gap-6 sm:grid-cols-3">
          {howItWorks.map((step, index) => (
            <li key={step.title} className="flex gap-3">
              <span className="bg-surface-raised text-muted-foreground flex size-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold tabular-nums">
                {index + 1}
              </span>
              <span className="min-w-0">
                <span className="block text-sm font-semibold">{step.title}</span>
                <span className="text-muted-foreground mt-1 block text-sm leading-6">
                  {step.caption}
                </span>
              </span>
            </li>
          ))}
        </ol>
      </section>
    </div>
  );
}

function RecentRoomRow({
  entry,
  status,
  onRejoin,
}: {
  entry: RecentRoom;
  status: RecentStatus;
  onRejoin: () => void;
}) {
  const live = status === "live";
  return (
    <li
      className={`surface-panel-subtle flex items-center gap-3 rounded-xl px-4 py-3 ${
        live ? "" : "opacity-60"
      }`}
    >
      <div className="bg-surface-raised flex size-10 shrink-0 items-center justify-center rounded-lg">
        <UsersRound className="text-muted-foreground size-4" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-semibold">{entry.title ?? "Watch Party"}</div>
        <div className="text-muted-foreground mt-0.5 flex items-center gap-2 text-xs">
          {live ? (
            <span className="flex items-center gap-1.5">
              <span aria-hidden="true" className="size-1.5 rounded-full bg-emerald-400" />
              Live
            </span>
          ) : status === "checking" ? (
            "Checking…"
          ) : status === "unknown" ? (
            "Status unavailable"
          ) : (
            "Ended"
          )}
          <span aria-hidden="true">·</span>
          <span>{entry.role === "host" ? "Your room" : "Joined"}</span>
          <span aria-hidden="true">·</span>
          <span className="font-mono tracking-[0.15em]">{entry.code}</span>
        </div>
      </div>
      {live || status === "unknown" ? (
        <Button type="button" size="sm" onClick={onRejoin}>
          Rejoin
        </Button>
      ) : (
        <span className="text-muted-foreground text-[11px] font-semibold tracking-[0.14em] uppercase">
          {status === "checking" ? "" : "Ended"}
        </span>
      )}
    </li>
  );
}
