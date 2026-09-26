import { captureSuggestionDraft } from "@/api/v2/watchTogetherSuggestionCreate";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { Users } from "lucide-react";
import { toast } from "sonner";
import { ApiClientError, StaleApiRequestContextError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useOptionalAuth } from "@/hooks/useAuth";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import {
  copyWatchTogetherInvite,
  endWatchTogetherRoom,
  setWatchTogetherGuestControl,
} from "@/lib/watchTogetherActions";
import { isRoomStaged, type WatchTogetherSuggestion } from "@/lib/watchTogether";
import { storage } from "@/utils/storage";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useWatchTogetherRoomConnection } from "@/player/hooks/useWatchTogetherRoomConnection";
import { useActivityFeed } from "./hooks/useActivityFeed";
import { markRecentRoomEnded, rememberRecentRoom } from "./hooks/useRecentRooms";
import { useRoomActions } from "./hooks/useRoomActions";
import { memberKey } from "./room/members";
import { RoomRail } from "./room/RoomRail";
import { RoomStatusStrip } from "./room/RoomStatusStrip";
import { LobbyEmptyStage, LobbyStagedStage, LobbySuggestions } from "./room/stage/LobbyStage";
import { PlayingStage } from "./room/stage/PlayingStage";
import { VotingStage } from "./room/stage/VotingStage";
import { BrowseShelf, type BrowseSelection } from "./room/browse/BrowseShelf";
import { CandidateStage, type CandidateChoice } from "./room/browse/CandidateStage";
import { itemHeadline } from "./room/stage/StagedHero";

function describeRoomError(error: unknown) {
  if (error === "not_found") return "Room not found.";
  if (error === "ended") return "That watch party is no longer active.";
  if (error === "forbidden") return "You don't have access to this watch party.";
  if (error === "host_left" || error === "room_closed") return "The room has ended.";
  if (error instanceof ApiClientError) {
    if (error.status === 404) return "Room not found.";
    if (error.status === 410) return "That room is no longer active.";
    return error.message;
  }
  return error instanceof Error ? error.message : "Room is unavailable.";
}

type WatchTogetherRoomLocationState = {
  suppressAutoStartSelection?: { contentId: string; fileId?: number; libraryId?: number };
  /** Set by the detail-page sheet in vote mode: the item becomes the first suggestion. */
  suggestFirst?: {
    content_id: string;
    content_type: "movie" | "episode";
    title: string;
    subtitle: string;
    poster_url: string;
  };
};

function RoomTerminalState({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: React.ReactNode;
}) {
  return (
    <div
      role="alert"
      className="mx-auto flex w-full max-w-5xl flex-col items-center gap-3 px-6 py-24 text-center"
    >
      <div className="bg-surface flex size-16 items-center justify-center rounded-2xl border border-white/10">
        <Users className="text-muted-foreground size-7" />
      </div>
      <h1 className="mt-2 text-xl font-semibold tracking-tight">{title}</h1>
      <p className="text-muted-foreground max-w-sm text-sm">{description}</p>
      <div className="mt-3 flex items-center gap-2">{children}</div>
    </div>
  );
}

/**
 * The room: a persistent stage on the left that changes with the phase, the
 * browse shelf under it, and a people/activity rail on the right. Browsing
 * puts a candidate on the stage; confirming there stages or suggests it.
 */
export default function WatchTogetherRoomPage() {
  useDocumentTitle("Watch Party");
  const { roomId } = useParams<{ roomId: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const roomToken = searchParams.get("room_token");
  const auth = useOptionalAuth();
  const userId = auth?.user?.id;
  const profileId = auth?.profile?.id;
  const playbackController = useWatchPlaybackController();
  const activePlaybackRequest = playbackController.state.request;
  // While the player is in the foreground for this same room it owns the
  // socket; a second connection here would double every dispatch.
  const suppressRoomConnection =
    playbackController.state.mode === "foreground" &&
    activePlaybackRequest?.roomId === roomId &&
    activePlaybackRequest?.roomToken === roomToken;
  const connection = useWatchTogetherRoomConnection({
    roomId: suppressRoomConnection ? null : roomId,
    roomToken: suppressRoomConnection ? null : roomToken,
  });
  const room = connection.room;
  const actions = useRoomActions(connection);
  const entries = useActivityFeed(room, connection.suggestions, connection.connectionState);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, []);

  const [candidate, setCandidate] = useState<BrowseSelection | null>(null);
  const [ending, setEnding] = useState(false);
  const [busySuggestion, setBusySuggestion] = useState<string | null>(null);

  const isHost = room?.self_can_manage_room === true;
  const isVote = room?.selection_mode === "vote";
  const isPlaying = room?.phase === "playing";
  const staged = isRoomStaged(room);
  const currentProfileId = storage.get(storage.KEYS.PROFILE_ID) ?? "";
  const memberNames = useMemo(
    () => new Map((room?.members ?? []).map((m) => [memberKey(m), m.display_name])),
    [room?.members],
  );

  // Remember the room for the hub and the detail page; forget it when it ends.
  const stagedDetail = useCatalogItemDetail(room?.selected_content_id, room?.selected_library_id);
  useEffect(() => {
    if (!room || !roomToken || !auth?.user || !auth.profile) return;
    rememberRecentRoom({
      room,
      token: roomToken,
      userId: auth.user.id,
      profileId: auth.profile.id,
      title: stagedDetail.data ? itemHeadline(stagedDetail.data) : undefined,
    });
  }, [room, roomToken, auth?.user, auth?.profile, stagedDetail.data]);
  useEffect(() => {
    if (roomId && connection.closedReason && userId != null && profileId) {
      markRecentRoomEnded({
        room_id: roomId,
        user_id: userId,
        profile_id: profileId,
      });
    }
  }, [roomId, connection.closedReason, userId, profileId]);

  // Auto-start: when the room starts playing a new selection, enter the
  // player. Keyed on the selection revision so a lobby restage never fires.
  const lastAutoStartRevisionRef = useRef<number | null>(null);
  const suppressAutoStartSelectionRef = useRef(
    (location.state as WatchTogetherRoomLocationState | null)?.suppressAutoStartSelection ?? null,
  );
  useEffect(() => {
    suppressAutoStartSelectionRef.current =
      (location.state as WatchTogetherRoomLocationState | null)?.suppressAutoStartSelection ?? null;
  }, [location.state]);
  useEffect(() => {
    if (!room || !roomId || !roomToken || connection.replacementReason) return;
    const suppressed = suppressAutoStartSelectionRef.current;
    if (suppressed) {
      suppressAutoStartSelectionRef.current = null;
      // Exit must reach the room even if the host started another selection
      // before this first snapshot arrived. Follow subsequent starts normally.
      lastAutoStartRevisionRef.current = room.selection_revision;
      return;
    }
    if (room.phase !== "playing" || !room.selected_content_id) return;
    if (lastAutoStartRevisionRef.current === room.selection_revision) return;
    lastAutoStartRevisionRef.current = room.selection_revision;
    // Started by the room, not by a Play press on this device.
    playbackController.startPlayback(
      {
        contentId: room.selected_content_id,
        fileId: room.selected_file_id,
        libraryId: room.selected_library_id,
        roomId,
        roomToken,
        restart: true,
      },
      "automatic",
    );
  }, [connection.replacementReason, playbackController, room, roomId, roomToken]);

  // Vote mode from a detail page: the sheet could not suggest before the
  // room existed, so it hands the item over and the room suggests it once,
  // with the room proof it now has.
  const suggestFirstRef = useRef(
    (location.state as WatchTogetherRoomLocationState | null)?.suggestFirst ?? null,
  );
  useEffect(() => {
    const first = suggestFirstRef.current;
    if (
      !first ||
      !room ||
      !roomId ||
      !roomToken ||
      room.selection_mode !== "vote" ||
      connection.replacementReason
    )
      return;
    suggestFirstRef.current = null;
    const draft = captureSuggestionDraft(roomId, roomToken, {
      content_id: first.content_id,
      content_type: first.content_type,
      title: first.title,
      subtitle: first.subtitle,
      poster_url: first.poster_url,
    });
    void connection.createSuggestion(draft).catch((error: unknown) => {
      if (!(error instanceof StaleApiRequestContextError))
        toast.error(error instanceof Error ? error.message : "Could not add the first suggestion");
    });
  }, [connection, room, roomId, roomToken]);

  const rejoinPlayback = useCallback(() => {
    if (!room?.selected_content_id || !roomId || !roomToken) return;
    lastAutoStartRevisionRef.current = room.selection_revision;
    playbackController.startPlayback(
      {
        contentId: room.selected_content_id,
        fileId: room.selected_file_id,
        libraryId: room.selected_library_id,
        roomId,
        roomToken,
        restart: true,
      },
      "viewer",
    );
  }, [playbackController, room, roomId, roomToken]);

  // What choosing from the shelf does: the host stages in a host-pick lobby;
  // everyone else, and everyone while voting or playing, suggests.
  const browseVerb: "stage" | "suggest" = isHost && !isVote && !isPlaying ? "stage" : "suggest";
  // A candidate belongs to one phase. Starting, stopping or switching modes
  // changes what confirming would mean, so it is dropped.
  const phaseKey = `${room?.phase ?? ""}:${room?.selection_mode ?? ""}`;
  const lastPhaseKeyRef = useRef(phaseKey);
  useEffect(() => {
    if (lastPhaseKeyRef.current !== phaseKey) {
      lastPhaseKeyRef.current = phaseKey;
      setCandidate(null);
    }
  }, [phaseKey]);
  const stageRef = useRef<HTMLDivElement | null>(null);
  // A staged lobby and a playing room fold the shelf to one line so the ready
  // check or the now-playing card owns the screen; "Change" reopens it.
  // The shelf stays mounted so folding preserves the viewer's search and filters.
  // Guests keep browsing in a staged lobby: suggesting is what they do while
  // the host decides.
  const shelfCollapsible = (staged && !isVote && isHost) || isPlaying;
  const [shelfOpen, setShelfOpen] = useState(false);
  useEffect(() => {
    if (!shelfCollapsible) setShelfOpen(false);
  }, [shelfCollapsible]);
  const selectCandidate = useCallback((selection: BrowseSelection) => {
    setCandidate(selection);
    // On one column the stage is above the shelf: bring the candidate up so
    // its action is never below the fold.
    stageRef.current?.scrollIntoView?.({ block: "start", behavior: "smooth" });
  }, []);

  const handleConfirm = useCallback(
    async (choice: CandidateChoice) => {
      if (!roomId || !roomToken) return;
      if (browseVerb === "stage") {
        const result = await actions.stage({
          content_id: choice.content_id,
          library_id: choice.library_id,
        });
        if (result !== null) setCandidate(null);
        return;
      }
      try {
        await connection.createSuggestion(
          captureSuggestionDraft(roomId, roomToken, {
            content_id: choice.content_id,
            content_type: choice.content_type,
            title: choice.title,
            subtitle: choice.subtitle,
            poster_url: choice.poster_url,
          }),
        );
        toast.success(`Suggested ${choice.title}`);
        setCandidate(null);
      } catch (error) {
        if (error instanceof StaleApiRequestContextError) return;
        toast.error(error instanceof Error ? error.message : "Could not suggest that");
      }
    },
    [actions, browseVerb, connection, roomId, roomToken],
  );

  const handleVoteToggle = useCallback(
    async (suggestion: WatchTogetherSuggestion) => {
      setBusySuggestion(suggestion.id);
      try {
        if (suggestion.voted_by_me) await connection.unvote(suggestion.id);
        else await connection.vote(suggestion.id);
      } catch (error) {
        if (!(error instanceof StaleApiRequestContextError))
          toast.error(error instanceof Error ? error.message : "Vote failed");
      } finally {
        setBusySuggestion(null);
      }
    },
    [connection],
  );
  const handleDelete = useCallback(
    async (id: string) => {
      setBusySuggestion(id);
      try {
        await connection.deleteSuggestion(id);
      } catch (error) {
        if (!(error instanceof StaleApiRequestContextError))
          toast.error(error instanceof Error ? error.message : "Could not remove that");
      } finally {
        setBusySuggestion(null);
      }
    },
    [connection],
  );
  // Host-pick lobbies still take suggestions; the host queues one by staging
  // its item, which is the same move as picking it from the shelf.
  const lobbySuggestions =
    room && !isVote && !isPlaying ? (
      <LobbySuggestions
        suggestions={connection.suggestions}
        isHost={isHost}
        currentProfileId={currentProfileId}
        memberNames={memberNames}
        busyId={busySuggestion}
        queueing={actions.busy === "stage"}
        onQueue={(suggestion) => void actions.stage({ content_id: suggestion.content_id })}
        onDelete={(id) => void handleDelete(id)}
      />
    ) : null;
  const handleCopyInvite = useCallback(() => {
    void copyWatchTogetherInvite(room?.invite_path, room?.code).then((copied) => {
      if (!copied) toast.error("Invite link isn't ready yet");
    });
  }, [room?.code, room?.invite_path]);
  const handleEnd = useCallback(async () => {
    setEnding(true);
    try {
      await endWatchTogetherRoom(connection.closeRoom);
    } finally {
      setEnding(false);
    }
  }, [connection.closeRoom]);

  if (!roomId || !roomToken) {
    return (
      <RoomTerminalState
        title="This invite link is incomplete"
        description="The link is missing its access token. Ask the host for a fresh invite, or join with a room code."
      >
        <Button type="button" onClick={() => navigate("/rooms")}>
          Join with a code
        </Button>
      </RoomTerminalState>
    );
  }
  if (connection.closedReason) {
    return (
      <RoomTerminalState
        title={describeRoomError(connection.closedReason)}
        description="Start a new watch party or join another room to keep watching together."
      >
        <Button type="button" onClick={() => navigate("/rooms")}>
          Back to Watch Party
        </Button>
      </RoomTerminalState>
    );
  }
  if (connection.replacementReason) {
    return (
      <RoomTerminalState
        title="Watch Party joined on another device"
        description={connection.replacementReason}
      >
        <Button type="button" onClick={connection.rejoinRoom}>
          Rejoin Watch Party
        </Button>
        <Button type="button" variant="outline" onClick={() => navigate("/rooms")}>
          Back to Watch Party
        </Button>
      </RoomTerminalState>
    );
  }

  return (
    <div className="flex h-[100dvh] min-h-[32rem] flex-col">
      <RoomStatusStrip
        room={room}
        connectionState={connection.connectionState}
        isHost={isHost}
        canSwitchMode={!isPlaying}
        onSwitchMode={(mode) => void actions.switchMode(mode)}
        onLeave={() => navigate("/rooms")}
        onEnd={handleEnd}
        ending={ending}
        onCopyInvite={room?.invite_path ? handleCopyInvite : undefined}
      />
      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <main className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="overlay-scroll flex min-h-0 min-w-0 flex-1 flex-col gap-5 overflow-x-hidden overflow-y-auto px-4 py-4 sm:px-5">
            {!room ? (
              <div className="text-muted-foreground flex flex-1 items-center justify-center text-sm">
                Connecting to the room…
              </div>
            ) : (
              <>
                <div ref={stageRef} className="min-w-0 scroll-mt-4">
                  {candidate ? (
                    <CandidateStage
                      selection={candidate}
                      roomId={roomId}
                      roomToken={roomToken}
                      members={room.members ?? []}
                      verb={browseVerb}
                      replaces={
                        browseVerb === "stage" && staged && stagedDetail.data
                          ? itemHeadline(stagedDetail.data)
                          : undefined
                      }
                      busy={actions.busy === "stage"}
                      onConfirm={(choice) => void handleConfirm(choice)}
                      onDismiss={() => setCandidate(null)}
                    />
                  ) : isPlaying && room.selected_content_id ? (
                    <PlayingStage
                      room={room}
                      suggestions={connection.suggestions}
                      serverTimeOffsetMs={connection.serverTimeOffsetMs}
                      isHost={isHost}
                      currentProfileId={currentProfileId}
                      memberNames={memberNames}
                      busyId={busySuggestion}
                      onRejoin={rejoinPlayback}
                      onStop={isHost ? () => void actions.stop() : undefined}
                      stopping={actions.busy === "stop"}
                      onVoteToggle={(s) => void handleVoteToggle(s)}
                      onDelete={(id) => void handleDelete(id)}
                    />
                  ) : isVote ? (
                    <VotingStage
                      room={room}
                      suggestions={connection.suggestions}
                      isHost={isHost}
                      currentProfileId={currentProfileId}
                      memberNames={memberNames}
                      busyId={busySuggestion}
                      promoting={actions.busy === "promote"}
                      onVoteToggle={(s) => void handleVoteToggle(s)}
                      onPromote={(id) => void actions.promote(id)}
                      onDelete={(id) => void handleDelete(id)}
                      onCopyInvite={handleCopyInvite}
                      onSetPolicy={(policy) =>
                        void setWatchTogetherGuestControl(connection.updatePolicy, policy)
                      }
                    />
                  ) : staged ? (
                    <div className="flex flex-col gap-4">
                      <LobbyStagedStage
                        room={room}
                        isHost={isHost}
                        busy={actions.busy !== null}
                        onStart={() => void actions.start()}
                        onChange={() => setShelfOpen(true)}
                        onSetReady={(ready) => actions.setReady(ready)}
                        onCopyInvite={handleCopyInvite}
                        onSetPolicy={(policy) =>
                          void setWatchTogetherGuestControl(connection.updatePolicy, policy)
                        }
                      />
                      {lobbySuggestions}
                    </div>
                  ) : (
                    <div className="flex flex-col gap-4">
                      <LobbyEmptyStage
                        room={room}
                        isHost={isHost}
                        onOpenVoting={() => void actions.switchMode("vote")}
                      />
                      {lobbySuggestions}
                    </div>
                  )}
                </div>
                <BrowseShelf
                  roomId={roomId}
                  roomToken={roomToken}
                  members={room.members ?? []}
                  selectedId={
                    candidate?.card.content_id ?? (staged ? room.selected_content_id : undefined)
                  }
                  verb={browseVerb === "stage" ? "pick" : "suggest"}
                  collapsible={shelfCollapsible}
                  collapsedLabel={
                    isPlaying ? "Suggest something for after" : "Change what's up next"
                  }
                  open={shelfOpen}
                  onOpenChange={setShelfOpen}
                  onSelect={selectCandidate}
                />
              </>
            )}
          </div>
        </main>
        <RoomRail
          room={room}
          entries={entries}
          now={now}
          footer={
            isHost
              ? "If you drop, the room waits 2 minutes for you before it ends."
              : room
                ? `Host: ${room.members?.find((m) => m.is_host)?.display_name ?? "unknown"} · ${
                    room.guest_control_policy === "guest_play_pause"
                      ? "guests can pause"
                      : "host controls playback"
                  }`
                : undefined
          }
        />
      </div>
    </div>
  );
}
