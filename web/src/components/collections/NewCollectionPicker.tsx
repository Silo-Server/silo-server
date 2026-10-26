import { useId, useRef, type CSSProperties, type ReactNode } from "react";
import { Link } from "react-router";
import { ChevronRight, GripVertical, ListOrdered, RefreshCw, WandSparkles } from "lucide-react";

import { StepDialog } from "@/components/calm/StepDialog";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminCollectionCapabilities } from "@/hooks/queries/admin/collections";
import { useCollectionCapabilities } from "@/hooks/queries/collections";
import {
  ADD_A_STARTER_PACK,
  KIND_GOOD_FOR,
  KIND_SENTENCE,
  NEW_COLLECTION,
  NEW_COLLECTION_HELP,
  NEW_COLLECTION_NEXT,
  STARTER_PACK_PROMPT,
  SYNCED_CHECK_FAILED,
  SYNCED_CHECKING,
  SYNCED_OFF,
} from "@/lib/collections/copy";
import { starterPacksHref } from "@/lib/collections/dialogs";
import { listReturnState, type ListReturnState } from "@/lib/collections/listReturn";
import { PERSONAL_SCOPE, SERVER_SCOPE, type ScopeKind } from "@/lib/collections/scope";
import { COLLECTION_KIND_LABEL, type CollectionKind } from "@/lib/collections/types";
import { cn } from "@/lib/utils";

const TINT: Readonly<Record<CollectionKind, string>> = {
  manual: "#5f74ee",
  smart: "#169e88",
  synced: "#bf7f22",
};

const ICON = { manual: ListOrdered, smart: WandSparkles, synced: RefreshCw } as const;

/** Stand-in poster art for the cards' pictures. */
const POSTERS = [
  "linear-gradient(160deg,#8fb7d8,#3c5b78)",
  "linear-gradient(160deg,#d8b54a,#7a5a18)",
  "linear-gradient(160deg,#4fa3e0,#1b4a7a)",
  "linear-gradient(160deg,#b8323a,#4a1014)",
  "linear-gradient(160deg,#2a2a30,#0c0c0f)",
];

function Poster({ index, className }: { index: number; className?: string }) {
  return (
    <span
      className={cn("block aspect-[2/3] shrink-0 rounded-md", className)}
      style={{ background: POSTERS[index % POSTERS.length] }}
    />
  );
}

/** The decorative picture at the top of each card, after the mockup. */
function Stage({ kind }: { kind: CollectionKind }) {
  const tile = "bg-white/[0.05] ring-1 ring-inset ring-white/[0.08]";
  let picture: ReactNode;
  if (kind === "manual") {
    picture = (
      <span className="absolute inset-x-[22px] top-5 grid gap-[7px]">
        {["Spirited Away", "My Neighbor Totoro", "Kiki's Delivery Service"].map((title, index) => (
          <span
            key={title}
            className={cn(
              "text-foreground/85 grid h-9 grid-cols-[14px_22px_1fr] items-center gap-2.5 rounded-[10px] px-2.5 text-[12.5px] font-medium",
              tile,
              index === 1 && "translate-x-2.5 -rotate-[1.5deg] bg-white/[0.09] shadow-lg",
            )}
          >
            <GripVertical className="text-muted-foreground size-[13px]" />
            <Poster index={index} className="w-[22px] rounded" />
            {title}
          </span>
        ))}
      </span>
    );
  } else if (kind === "smart") {
    picture = (
      <span className="absolute inset-x-[18px] top-[18px] grid gap-3">
        <span className="flex gap-1.5">
          {["Genre is Comedy", "Year 1990–1999", "+1 rule"].map((rule, index) => (
            <span
              key={rule}
              className={cn(
                "text-foreground/85 inline-flex h-6 items-center rounded-[7px] px-[9px] text-[11.5px] font-medium whitespace-nowrap",
                index < 2 ? "bg-[color-mix(in_srgb,var(--tint)_22%,transparent)]" : tile,
              )}
            >
              {rule}
            </span>
          ))}
        </span>
        <span className="flex gap-[7px]">
          {POSTERS.map((_, index) => (
            <Poster
              key={index}
              index={index}
              className={cn("w-[50px]", index === 3 && "ring-2 ring-(--tint)")}
            />
          ))}
        </span>
      </span>
    );
  } else {
    picture = (
      <span className="absolute inset-x-[18px] top-[18px] grid gap-2.5">
        {[
          { mark: "M", color: "#1f6feb", name: "IMDb Top 250 Movies" },
          { mark: "T", color: "#0d8a6a", name: "Trending this week" },
        ].map(({ mark, color, name }) => (
          <span
            key={name}
            className={cn(
              "flex h-9 items-center gap-2.5 rounded-[11px] px-3 text-[12.5px] font-semibold",
              tile,
            )}
          >
            <span
              className="grid size-6 place-items-center rounded-md text-[10px] font-extrabold text-white"
              style={{ background: color }}
            >
              {mark}
            </span>
            {name}
            <span className="text-muted-foreground ml-auto inline-flex items-center gap-1 font-medium">
              <RefreshCw className="size-3" />
              Daily
            </span>
          </span>
        ))}
        <span className="flex gap-[7px]">
          {POSTERS.map((_, index) => (
            <Poster key={index} index={index + 2} className="w-9" />
          ))}
        </span>
      </span>
    );
  }
  return (
    <span
      aria-hidden
      className="border-border/80 relative block h-[168px] overflow-hidden border-b bg-[radial-gradient(120%_120%_at_0%_0%,color-mix(in_srgb,var(--tint)_22%,transparent),transparent_62%)] max-lg:hidden"
    >
      {picture}
    </span>
  );
}

const CARD =
  "group border-border/90 bg-surface/60 relative flex flex-col overflow-hidden rounded-[18px] border text-left outline-none";

/** Name, what it does and examples: the part of a card under its picture. */
function CardBody({
  kind,
  scope,
  nameId,
  sentenceId,
  noteId,
  note,
}: {
  kind: CollectionKind;
  scope: ScopeKind;
  nameId: string;
  sentenceId: string;
  noteId: string;
  /** Replaces the examples, for a type that can't be made. */
  note?: string;
}) {
  const Icon = ICON[kind];
  return (
    <span className="grid gap-1.5 px-5 pt-[18px] pb-5 max-lg:pr-11">
      <span className="flex items-center gap-2.5 text-[17px] font-semibold tracking-[-0.015em]">
        <span
          aria-hidden
          className="grid size-8 place-items-center rounded-[9px] bg-[color-mix(in_srgb,var(--tint)_18%,transparent)] text-(--tint)"
        >
          <Icon className="size-4" />
        </span>
        <span id={nameId}>{COLLECTION_KIND_LABEL[kind]}</span>
      </span>
      <span id={sentenceId} className="text-foreground/75 text-[13.5px] leading-normal">
        {KIND_SENTENCE[kind]}
      </span>
      <span id={noteId} className="text-muted-foreground mt-1.5 text-[12.5px] leading-normal">
        {note ?? (
          <>
            <b className="text-foreground/70 font-medium">Good for</b> {KIND_GOOD_FOR[scope][kind]}
          </>
        )}
      </span>
    </span>
  );
}

/** One type, as a link to its editor page. Lifts and shows its chevron only on hover or focus. */
function TypeCard({
  kind,
  scope,
  to,
  state,
}: {
  kind: CollectionKind;
  scope: ScopeKind;
  to: string;
  state?: ListReturnState;
}) {
  const id = useId();
  return (
    <Link
      to={to}
      state={state}
      aria-labelledby={`${id}-name`}
      aria-describedby={`${id}-sentence ${id}-note`}
      style={{ "--tint": TINT[kind] } as CSSProperties}
      className={cn(
        CARD,
        "transition-[translate,background-color,border-color,box-shadow] duration-150",
        "hover:border-foreground/35 hover:bg-accent focus-visible:border-foreground/35 focus-visible:bg-accent hover:-translate-y-0.5 hover:shadow-xl focus-visible:-translate-y-0.5 focus-visible:shadow-xl",
        "focus-visible:ring-ring/50 focus-visible:ring-[3px]",
      )}
    >
      <Stage kind={kind} />
      <CardBody
        kind={kind}
        scope={scope}
        nameId={`${id}-name`}
        sentenceId={`${id}-sentence`}
        noteId={`${id}-note`}
      />
      <ChevronRight
        aria-hidden
        className="text-muted-foreground absolute right-[18px] bottom-5 size-4 opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100 max-lg:opacity-100"
      />
    </Link>
  );
}

/** Synced list when this server can't make one: the card, dimmed, with the reason. */
function SyncedOffCard({ scope }: { scope: ScopeKind }) {
  const id = useId();
  return (
    <div style={{ "--tint": TINT.synced } as CSSProperties} className={cn(CARD, "opacity-60")}>
      <Stage kind="synced" />
      <CardBody
        kind="synced"
        scope={scope}
        nameId={`${id}-name`}
        sentenceId={`${id}-sentence`}
        noteId={`${id}-note`}
        note={SYNCED_OFF}
      />
    </div>
  );
}

/**
 * New collection: Manual, Smart or Synced list, each a link to the editor page
 * for that type. Synced list waits for the scope's capabilities, so it never
 * shows and then turns off. Admins can add a whole starter pack instead.
 */
export function NewCollectionPicker({
  scope,
  libraryId = null,
  listHref,
  onClose,
}: {
  scope: ScopeKind;
  /** The library pill selected on the list, carried to the editor. */
  libraryId?: number | null;
  /** The list as it is under the picker; the editor's Back and Starter packs return to it. */
  listHref?: string;
  onClose: () => void;
}) {
  const isServer = scope === "server";
  const paths = (isServer ? SERVER_SCOPE : PERSONAL_SCOPE).paths;
  const returnTo = listHref ?? paths.list({ libraryId });
  // Only the server editor reads where it was opened from.
  const linkState = isServer ? listReturnState(returnTo) : undefined;
  const adminCapabilities = useAdminCollectionCapabilities(isServer);
  const personalCapabilities = useCollectionCapabilities(!isServer);
  const capabilities = isServer ? adminCapabilities : personalCapabilities;
  const syncedOn = capabilities.data ? capabilities.data.import_sources.length > 0 : undefined;
  const titleRef = useRef<HTMLHeadingElement>(null);

  let synced: ReactNode;
  if (syncedOn) {
    synced = (
      <TypeCard
        kind="synced"
        scope={scope}
        to={paths.create({ type: "synced", libraryId })}
        state={linkState}
      />
    );
  } else if (syncedOn === false) {
    synced = <SyncedOffCard scope={scope} />;
  } else if (capabilities.isError) {
    synced = (
      <div className={cn(CARD, "items-start justify-center gap-3 p-5")}>
        <p className="text-muted-foreground text-sm">{SYNCED_CHECK_FAILED}</p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={capabilities.isFetching}
          onClick={() => void capabilities.refetch()}
        >
          Retry
        </Button>
      </div>
    );
  } else {
    synced = (
      <div role="status" aria-label={SYNCED_CHECKING} className={cn(CARD, "gap-3 p-5")}>
        <Skeleton className="h-[128px] rounded-xl max-lg:hidden" />
        <Skeleton className="h-6 w-32" />
        <Skeleton className="h-4 w-full" />
      </div>
    );
  }

  const starterPacks = isServer && syncedOn ? starterPacksHref(returnTo) : null;

  return (
    <StepDialog
      size="choice"
      onClose={onClose}
      // No card is highlighted at rest; Tab reaches Manual first.
      onOpenFocus={() => titleRef.current?.focus()}
      title={NEW_COLLECTION}
      titleRef={titleRef}
      description={NEW_COLLECTION_HELP[scope]}
      footerStart={
        <p className="text-muted-foreground min-w-0 text-[13px]">
          {NEW_COLLECTION_NEXT}
          {starterPacks ? (
            <>
              <span aria-hidden className="mx-2">
                ·
              </span>
              {STARTER_PACK_PROMPT}{" "}
              <Link
                to={starterPacks}
                replace
                className="text-foreground font-medium underline underline-offset-4"
              >
                {ADD_A_STARTER_PACK}
              </Link>
            </>
          ) : null}
        </p>
      }
    >
      <div className="grid min-h-0 gap-3.5 overflow-y-auto px-5 pt-1 pb-6 sm:px-7 lg:grid-cols-3">
        <TypeCard
          kind="manual"
          scope={scope}
          to={paths.create({ type: "manual", libraryId })}
          state={linkState}
        />
        <TypeCard
          kind="smart"
          scope={scope}
          to={paths.create({ type: "smart", libraryId })}
          state={linkState}
        />
        {synced}
      </div>
    </StepDialog>
  );
}
