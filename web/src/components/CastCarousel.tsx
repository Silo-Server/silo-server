import { Fragment, memo, useMemo } from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import type { CastMember } from "@/api/types";
import type { CrewGroup } from "@/components/castCrewGroups";
import { usePrefetchPeople } from "@/hooks/queries/people";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { buildPersonCatalogHref } from "@/pages/catalogSearchParams";
import { getInitials } from "@/lib/text";
import { cn } from "@/lib/utils";

interface CastCarouselProps {
  cast: CastMember[];
  limit?: number;
  /**
   * When true, the carousel adds MediaCarousel-style edge padding so it can sit
   * at the top level of a page (outside `.page-shell`) and align with other
   * full-bleed rows.
   */
  fullBleed?: boolean;
  /** Warm person detail for the shown cast so opening one renders at once. */
  prefetchPeople?: boolean;
  /**
   * Crew groups shown ahead of the cast, in order (e.g. Director, then
   * Writers). Each group after the first, and the cast, gets a labelled
   * divider so the row reads as one "Cast & Crew" strip.
   */
  crewGroups?: CrewGroup[];
}

interface CreditCardData {
  name: string;
  subtitle: string;
  personId: string;
  photoUrl?: string;
}

interface CreditSection {
  key: string;
  label: string;
  cards: CreditCardData[];
}

function CastCarousel({
  cast,
  limit = 20,
  fullBleed = false,
  prefetchPeople = false,
  crewGroups,
}: CastCarouselProps) {
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla();
  const visible = useMemo(
    () =>
      cast
        .slice()
        .sort((a, b) => a.order - b.order)
        .slice(0, limit),
    [cast, limit],
  );
  const sections = useMemo(() => {
    const out: CreditSection[] = [];
    for (const group of crewGroups ?? []) {
      const seen = new Set<string>();
      const cards: CreditCardData[] = [];
      for (const member of group.members) {
        const key = member.person_id || member.name;
        if (seen.has(key)) continue;
        seen.add(key);
        cards.push({
          name: member.name,
          subtitle: group.role,
          personId: member.person_id,
          photoUrl: member.photo_url,
        });
        if (cards.length === group.max) break;
      }
      if (cards.length > 0) {
        out.push({ key: group.label, label: group.label, cards });
      }
    }
    if (visible.length > 0) {
      out.push({
        key: "cast",
        label: "Cast",
        cards: visible.map((member) => ({
          name: member.name,
          subtitle: member.character,
          personId: member.person_id,
          photoUrl: member.photo_url,
        })),
      });
    }
    return out;
  }, [crewGroups, visible]);
  const prefetchIds = useMemo(
    () =>
      sections.flatMap((section) => section.cards.flatMap((c) => (c.personId ? [c.personId] : []))),
    [sections],
  );

  if (sections.length === 0) return null;

  return (
    <div className="group/carousel relative">
      {prefetchPeople && <PrefetchCastPeople personIds={prefetchIds} />}
      {canScrollPrev && (
        <button
          type="button"
          onClick={scrollPrev}
          className={cn(
            "from-background/90 absolute top-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100",
            fullBleed ? "left-4 sm:left-6 lg:left-10 xl:left-12" : "left-0",
          )}
          aria-label="Scroll left"
        >
          <ChevronLeft className="text-foreground h-6 w-6" />
        </button>
      )}

      <div
        ref={emblaRef}
        className={cn(
          "embla__viewport -mt-1 overflow-hidden pt-1",
          fullBleed && "pr-4 sm:pr-6 lg:pr-10 xl:pr-12",
        )}
      >
        <ul
          role="list"
          className={cn(
            "embla__container flex cursor-grab list-none gap-3",
            fullBleed && "pl-4 sm:pl-6 lg:pl-10 xl:pl-12",
          )}
        >
          {sections.map((section, sectionIndex) => (
            <Fragment key={section.key}>
              {sectionIndex > 0 && (
                <li
                  aria-hidden="true"
                  className="embla__slide flex shrink-0 items-start self-stretch px-3 pb-10"
                >
                  <div className="flex h-full items-center gap-2">
                    <span className="text-muted-foreground/70 rotate-180 text-[10px] font-semibold tracking-[0.2em] uppercase [writing-mode:vertical-rl]">
                      {section.label}
                    </span>
                    <span className="bg-border h-full w-px" />
                  </div>
                </li>
              )}
              {section.cards.map((card, i) => (
                <li
                  key={`${section.key}-${card.personId || card.name}-${i}`}
                  className="embla__slide shrink-0"
                >
                  <CastCard data={card} />
                </li>
              ))}
            </Fragment>
          ))}
        </ul>
      </div>

      {canScrollNext && (
        <button
          type="button"
          onClick={scrollNext}
          className={cn(
            "from-background/90 absolute top-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100",
            fullBleed ? "right-4 sm:right-6 lg:right-10 xl:right-12" : "right-0",
          )}
          aria-label="Scroll right"
        >
          <ChevronRight className="text-foreground h-6 w-6" />
        </button>
      )}
    </div>
  );
}

export default memo(CastCarousel);

function PrefetchCastPeople({ personIds }: { personIds: string[] }) {
  usePrefetchPeople(personIds);
  return null;
}

function CastCard({ data: member }: { data: CreditCardData }) {
  const href = member.personId ? buildPersonCatalogHref(member.personId) : null;
  const inner = (
    <>
      <div className="media-card-image mb-2.5 aspect-[2/3] overflow-hidden rounded-lg">
        {member.photoUrl ? (
          <img
            src={member.photoUrl}
            alt={member.name}
            className="h-full w-full object-cover transition-transform duration-300 group-hover/cast:scale-105"
            loading="lazy"
            decoding="async"
          />
        ) : (
          <div className="bg-surface text-muted-foreground flex h-full w-full items-center justify-center text-lg font-semibold">
            {getInitials(member.name)}
          </div>
        )}
      </div>
      <div className="px-0.5">
        <div className="text-foreground truncate text-[13px] font-medium">{member.name}</div>
        {member.subtitle ? (
          <div className="text-muted-foreground truncate text-[11px]">{member.subtitle}</div>
        ) : null}
      </div>
    </>
  );

  if (href) {
    return (
      <ViewTransitionLink to={href} className="group/cast block w-[110px]">
        {inner}
      </ViewTransitionLink>
    );
  }
  return <div className="group/cast w-[110px]">{inner}</div>;
}
