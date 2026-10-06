import { Navigate, useLocation, useParams } from "react-router";

/**
 * `/requests/:mediaType/:tmdbId` → `/title/:mediaType/:tmdbId`. Older
 * notifications and bookmarks still link to the request path. The segments
 * pass through unchecked, so a bad media type lands on the title page's
 * unavailable state instead of being read as a movie.
 */
export default function LegacyRequestDetailRedirect() {
  const { mediaType = "", tmdbId = "" } = useParams<{ mediaType: string; tmdbId: string }>();
  const { search, hash } = useLocation();
  return (
    <Navigate
      to={`/title/${encodeURIComponent(mediaType)}/${encodeURIComponent(tmdbId)}${search}${hash}`}
      replace
    />
  );
}
