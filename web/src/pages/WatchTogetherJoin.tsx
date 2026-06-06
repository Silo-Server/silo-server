import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { ApiClientError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import {
  createWatchTogetherRoom,
  joinWatchTogetherRoom,
  type WatchTogetherSelectionMode,
} from "@/lib/watchTogether";

function describeJoinError(error: unknown) {
  if (error instanceof ApiClientError) {
    if (error.status === 404) {
      return "Room not found.";
    }
    if (error.status === 410) {
      return "That room is no longer active.";
    }
    return error.message;
  }

  return error instanceof Error ? error.message : "Failed to join room.";
}

export default function WatchTogetherJoin() {
  useDocumentTitle("Watch Party");
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const token = searchParams.get("token")?.trim() ?? "";
  const [code, setCode] = useState("");
  const [selectionMode, setSelectionMode] = useState<WatchTogetherSelectionMode>("host_pick");
  const [creating, setCreating] = useState(false);
  const [joining, setJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const hasInviteToken = token !== "";

  const joinRoom = useCallback(
    async (input: { code?: string; join_token?: string }) => {
      setJoining(true);
      setError(null);
      try {
        const response = await joinWatchTogetherRoom(input);
        if (!response.room_access_token) {
          throw new Error("Room access token was missing from the join response.");
        }
        navigate(`/rooms/${response.room.room_id}?room_token=${response.room_access_token}`, {
          replace: true,
          viewTransition: true,
        });
      } catch (joinError) {
        setError(describeJoinError(joinError));
      } finally {
        setJoining(false);
      }
    },
    [navigate],
  );

  const createRoom = useCallback(async () => {
    setCreating(true);
    setError(null);
    try {
      const response = await createWatchTogetherRoom({ selection_mode: selectionMode });
      if (!response.room_access_token) {
        throw new Error("Room access token was missing from the create response.");
      }
      navigate(`/rooms/${response.room.room_id}?room_token=${response.room_access_token}`, {
        replace: true,
        viewTransition: true,
      });
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : "Failed to create room.");
    } finally {
      setCreating(false);
    }
  }, [navigate, selectionMode]);

  useEffect(() => {
    if (!hasInviteToken) {
      return;
    }

    void joinRoom({ join_token: token });
  }, [hasInviteToken, joinRoom, token]);

  const headline = useMemo(
    () => (hasInviteToken ? "Joining Watch Party" : "Create or Join a Watch Party"),
    [hasInviteToken],
  );

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-10 px-6 py-10 sm:px-8">
      <div className="max-w-2xl">
        <p className="text-muted-foreground text-[11px] font-semibold tracking-[0.18em] uppercase">
          Watch Party
        </p>
        <h1 className="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">{headline}</h1>
        <p className="text-muted-foreground mt-3 max-w-2xl text-sm leading-6">
          Start an empty room, invite people in, then choose what everyone watches from the lobby.
          Invite links still open the same flow and take guests into the room automatically.
        </p>
      </div>

      {/* ─── How it works ─── */}
      <div className="grid gap-4 sm:grid-cols-3">
        <div className="surface-panel-subtle rounded-xl p-4">
          <div className="text-foreground/50 mb-1.5 text-[11px] font-semibold tracking-[0.15em] uppercase">
            Synced Playback
          </div>
          <p className="text-foreground/65 text-sm leading-relaxed">
            Everyone watches in sync. When someone seeks or the host picks new content, playback
            pauses while all participants buffer, then resumes together.
          </p>
        </div>
        <div className="surface-panel-subtle rounded-xl p-4">
          <div className="text-foreground/50 mb-1.5 text-[11px] font-semibold tracking-[0.15em] uppercase">
            Playback Controls
          </div>
          <p className="text-foreground/65 text-sm leading-relaxed">
            By default only the host can play, pause, and seek. The host can enable "Allow Pause" to
            let guests pause and resume, but seeking stays host-only.
          </p>
        </div>
        <div className="surface-panel-subtle rounded-xl p-4">
          <div className="text-foreground/50 mb-1.5 text-[11px] font-semibold tracking-[0.15em] uppercase">
            Room & Invites
          </div>
          <p className="text-foreground/65 text-sm leading-relaxed">
            Share the room code or invite link to let others join. If the host disconnects the room
            stays open for 15 seconds — if they don't reconnect, the party ends automatically.
          </p>
        </div>
      </div>

      {error ? (
        <div className="rounded-[8px] border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-200">
          {error}
        </div>
      ) : null}

      <div className="grid gap-5 lg:grid-cols-2">
        <section className="surface-panel rounded-xl p-5">
          <div className="flex flex-col gap-3">
            <div>
              <h2 className="text-lg font-semibold">Create a room</h2>
              <p className="text-foreground/55 mt-1 text-sm leading-6">
                Open a lobby, copy the invite, and pick the movie or episode once everyone is in.
              </p>
            </div>

            <div>
              <label className="text-sm font-medium">Selection mode</label>
              <div className="mt-2 flex gap-2" role="radiogroup">
                <button
                  type="button"
                  role="radio"
                  aria-checked={selectionMode === "host_pick"}
                  onClick={() => setSelectionMode("host_pick")}
                  className={`flex-1 rounded-[8px] border px-3 py-2.5 text-left text-sm transition-colors ${
                    selectionMode === "host_pick"
                      ? "border-foreground/50 bg-accent font-medium"
                      : "text-muted-foreground hover:bg-muted/60"
                  }`}
                >
                  <div className="font-medium">Host Picks</div>
                  <div className="text-muted-foreground mt-0.5 text-xs">
                    Host decides what to watch.
                  </div>
                </button>
                <button
                  type="button"
                  role="radio"
                  aria-checked={selectionMode === "vote"}
                  onClick={() => setSelectionMode("vote")}
                  className={`flex-1 rounded-[8px] border px-3 py-2.5 text-left text-sm transition-colors ${
                    selectionMode === "vote"
                      ? "border-foreground/50 bg-accent font-medium"
                      : "text-muted-foreground hover:bg-muted/60"
                  }`}
                >
                  <div className="font-medium">Vote Together</div>
                  <div className="text-muted-foreground mt-0.5 text-xs">
                    Everyone suggests and votes.
                  </div>
                </button>
              </div>
            </div>

            <Button
              type="button"
              onClick={() => void createRoom()}
              disabled={creating || joining}
              className="h-11 rounded-[8px] px-5"
            >
              {creating ? "Creating..." : "Create Watch Party"}
            </Button>
          </div>
        </section>

        <section className="surface-panel rounded-xl p-5">
          <div className="grid gap-4">
            <div>
              <h2 className="text-lg font-semibold">Join a room</h2>
              <p className="text-foreground/55 mt-1 text-sm leading-6">
                Enter a room code or retry an invite link to enter the existing party.
              </p>
            </div>

            <div className="grid gap-2">
              <label htmlFor="watch-room-code" className="text-sm font-medium">
                Room code
              </label>
              <Input
                id="watch-room-code"
                value={code}
                onChange={(event) => {
                  setCode(event.target.value.toUpperCase());
                  if (error) {
                    setError(null);
                  }
                }}
                maxLength={12}
                autoCapitalize="characters"
                autoCorrect="off"
                spellCheck={false}
                placeholder="ABCD1234"
                disabled={joining || creating}
                className="h-11 text-sm tracking-[0.18em] uppercase"
              />
            </div>

            <Button
              type="button"
              onClick={() => void joinRoom({ code: code.trim().toUpperCase() })}
              disabled={joining || creating || code.trim() === ""}
              className="h-11 rounded-[8px] px-5"
            >
              {joining ? "Joining..." : "Join Watch Party"}
            </Button>

            {hasInviteToken ? (
              <Button
                type="button"
                variant="outline"
                onClick={() => void joinRoom({ join_token: token })}
                disabled={joining || creating}
                className="h-11 rounded-[8px] px-5"
              >
                Retry Invite Link
              </Button>
            ) : null}
          </div>
        </section>
      </div>
    </div>
  );
}
