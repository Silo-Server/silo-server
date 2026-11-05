import { forwardRef, useState, type ComponentProps } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { isNotFoundProblem } from "@/api/v2/request";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { fetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { catalogKeys } from "@/hooks/queries/keys";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";

type LibraryItemLinkProps = Omit<ComponentProps<typeof ViewTransitionLink>, "to"> & {
  contentId: string;
};

/**
 * Links to a library item that the server matched without checking this
 * viewer's access, as a request's library copy is. A plain click reads the
 * item first and stays put when the viewer cannot open it; the read warms the
 * item page's own query. Modified clicks open the item page directly.
 */
const LibraryItemLink = forwardRef<HTMLAnchorElement, LibraryItemLinkProps>(
  function LibraryItemLink({ contentId, onClick, ...rest }, ref) {
    const queryClient = useQueryClient();
    const navigate = useViewTransitionNavigate();
    const [checking, setChecking] = useState(false);
    const to = `/item/${encodeURIComponent(contentId)}`;

    return (
      <ViewTransitionLink
        ref={ref}
        to={to}
        aria-busy={checking || undefined}
        onClick={(event) => {
          onClick?.(event);
          if (
            event.defaultPrevented ||
            event.button !== 0 ||
            event.metaKey ||
            event.ctrlKey ||
            event.shiftKey ||
            event.altKey
          ) {
            return;
          }
          event.preventDefault();
          if (checking) return;
          setChecking(true);
          queryClient
            .fetchQuery({
              queryKey: catalogKeys.itemDetail(contentId),
              queryFn: ({ signal }) => fetchCatalogItemDetail(contentId, undefined, { signal }),
            })
            .then(
              () => navigate(to),
              (error: unknown) => {
                if (isNotFoundProblem(error)) {
                  toast.error("This title is in a library this profile can't open.");
                  return;
                }
                // A failed check says nothing about access; the item page answers.
                navigate(to);
              },
            )
            .finally(() => setChecking(false));
        }}
        {...rest}
      />
    );
  },
);

export default LibraryItemLink;
