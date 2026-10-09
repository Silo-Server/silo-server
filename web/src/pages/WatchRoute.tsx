import { useContext, useEffect, useMemo } from "react";
import { useLocation, useParams, useSearchParams } from "react-router";
import { WatchPlaybackControllerContext } from "@/playback/watchPlaybackContext";
import { createWatchRouteRequest } from "./watchRouteHelpers";

/**
 * WatchRoute is a route controller only.
 * The app-level playback host owns the persistent player instance.
 */
export default function WatchRoute() {
  const controller = useContext(WatchPlaybackControllerContext);
  const syncRouteRequest = controller?.syncRouteRequest;
  const handleRouteExit = controller?.handleRouteExit;
  const location = useLocation();
  const { id } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const request = useMemo(() => {
    if (!id) {
      return null;
    }

    const fileIdParam = searchParams.get("fileId");
    const libraryIdParam = searchParams.get("libraryId");
    const locationState = location.state as {
      watchReturnHref?: string;
      audioTrackIndex?: number;
      prePlaySubtitleMode?: "auto" | "off" | "explicit";
      prePlaySubtitleSelection?: {
        source: "embedded" | "external" | "downloaded";
        language?: string;
        codec?: string;
        label?: string;
        forced?: boolean;
        hearing_impaired?: boolean;
        external_subtitle_path?: string;
        downloaded_subtitle_id?: number;
      } | null;
    } | null;

    return createWatchRouteRequest({
      contentId: id,
      fileId: fileIdParam ? Number.parseInt(fileIdParam, 10) : undefined,
      libraryId: libraryIdParam ? Number.parseInt(libraryIdParam, 10) : undefined,
      roomId: searchParams.get("room_id") ?? undefined,
      roomToken: searchParams.get("room_token") ?? undefined,
      shuffleId: searchParams.get("shuffle") ?? undefined,
      restart: searchParams.get("restart") === "1",
      audioTrackIndex: locationState?.audioTrackIndex,
      prePlaySubtitleMode: locationState?.prePlaySubtitleMode,
      prePlaySubtitleSelection: locationState?.prePlaySubtitleSelection,
      returnHref: locationState?.watchReturnHref,
    });
  }, [id, location.state, searchParams]);

  useEffect(() => {
    if (!syncRouteRequest || !request) {
      return;
    }

    syncRouteRequest(request);
  }, [request, syncRouteRequest]);

  useEffect(() => {
    if (!handleRouteExit || !request) {
      return;
    }

    return () => {
      handleRouteExit(request.requestKey);
    };
  }, [handleRouteExit, request]);

  return null;
}
