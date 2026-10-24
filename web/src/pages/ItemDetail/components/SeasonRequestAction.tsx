import RequestSeasonButton from "./RequestSeasonButton";

/**
 * The season page's own Request, for a season with episodes the library
 * lacks. Requested shows even when this profile cannot request.
 */
export default function SeasonRequestAction({
  seasonNumber,
  onRequestSeason,
  requested,
}: {
  seasonNumber: number;
  onRequestSeason?: (seasonNumber: number) => void;
  requested: boolean;
}) {
  if (!onRequestSeason && !requested) return null;
  return (
    <RequestSeasonButton
      target={`Season ${seasonNumber}`}
      requested={requested}
      onRequest={() => onRequestSeason?.(seasonNumber)}
      alwaysVisible
    />
  );
}
