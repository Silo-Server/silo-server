import { Navigate, useParams } from "react-router";
import type { RequestMediaType } from "@/api/types";
import { isNotFoundProblem } from "@/api/v2/request";
import PageUnavailable from "@/components/PageUnavailable";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useRequestMediaDetail } from "@/hooks/queries/useRequests";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { parseRequestMediaType } from "@/lib/mediaRequests";
import ExternalTitleContent from "@/pages/ItemDetail/ExternalTitleContent";
import ItemDetailSkeleton from "@/pages/ItemDetail/ItemDetailSkeleton";
import { useT } from "@/i18n";

/**
 * `/title/:mediaType/:tmdbId`: a movie or series by its TMDB id. A title the
 * viewer can open in the library redirects to its item page; any other renders
 * like one, with request actions in place of playback.
 */
export default function TitleDetail() {
  const params = useParams<{ mediaType: string; tmdbId: string }>();
  const mediaType = parseRequestMediaType(params.mediaType);
  const tmdbID = /^\d+$/.test(params.tmdbId ?? "") ? Number(params.tmdbId) : 0;

  if (!mediaType || tmdbID <= 0) {
    return <TitleUnavailable />;
  }
  // Keyed so another title starts from its own loading state.
  return <TitlePage key={`${mediaType}:${tmdbID}`} mediaType={mediaType} tmdbID={tmdbID} />;
}

function TitlePage({ mediaType, tmdbID }: { mediaType: RequestMediaType; tmdbID: number }) {
  const t = useT();
  const detail = useRequestMediaDetail(mediaType, tmdbID);
  const notFound = isNotFoundProblem(detail.error);
  const item = notFound ? undefined : detail.data;
  const libraryContentID = item?.library_content_id;
  // The library copy may sit in a library this viewer cannot open, and the
  // catalog answers 404 for it then. Only a copy that loads is worth leaving
  // this page for.
  const libraryItem = useCatalogItemDetail(libraryContentID);

  useDocumentTitle(
    notFound
      ? t("web.detail.page.notFoundTitle")
      : (item?.title ?? t("web.detail.page.titleTabFallback")),
  );

  if (detail.isLoading || (libraryContentID && libraryItem.isLoading)) {
    return <ItemDetailSkeleton />;
  }

  if (!item) {
    if (notFound || !detail.isError) {
      return <TitleUnavailable />;
    }
    return (
      <PageUnavailable
        title={t("web.detail.page.titleLoadFailed")}
        description={t("web.detail.page.loadFailedBody")}
        onRetry={() => void detail.refetch()}
        retrying={detail.isFetching}
      />
    );
  }

  if (libraryContentID && libraryItem.data) {
    return <Navigate to={`/item/${encodeURIComponent(libraryContentID)}`} replace />;
  }

  // A failed check says nothing about access, so the item page gets to answer.
  const libraryHref =
    libraryContentID && libraryItem.isError && !isNotFoundProblem(libraryItem.error)
      ? `/item/${encodeURIComponent(libraryContentID)}`
      : undefined;

  return <ExternalTitleContent item={item} libraryHref={libraryHref} />;
}

/**
 * The detail API answers 404 for a title outside the viewer's rating limit as
 * well as for one TMDB does not know, so this says neither, like the item page.
 */
function TitleUnavailable() {
  const t = useT();
  useDocumentTitle(t("web.detail.page.notFoundTitle"));
  return (
    <PageUnavailable
      title={t("web.detail.page.unavailableTitle")}
      description={t("web.detail.page.unavailableBody")}
    />
  );
}
