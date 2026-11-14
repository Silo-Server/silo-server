import { forwardRef, type ReactNode } from "react";
import { Link } from "react-router";
import {
  ChevronDown,
  ChevronLeft,
  Eye,
  ListOrdered,
  Loader2,
  RefreshCw,
  Trash2,
  Users,
  WandSparkles,
  type LucideIcon,
} from "lucide-react";

import { ActionMenu, type ActionMenuAction } from "@/components/calm/ActionMenu";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  newCollection,
  notCreatedYet,
  saveBeforeSync,
  syncNow,
  syncingNow,
} from "@/lib/collections/copy";
import { cn } from "@/lib/utils";
import { collectionKindLabel, type CollectionKind } from "@/lib/collections/types";
import { useT } from "@/i18n";

const KIND_ICON: Record<CollectionKind, LucideIcon> = {
  manual: ListOrdered,
  smart: WandSparkles,
  synced: RefreshCw,
};

/** The menu's spinner while a sync runs. */
const Spinner = forwardRef<SVGSVGElement, { className?: string }>(function Spinner(
  { className, ...props },
  ref,
) {
  return <Loader2 ref={ref} {...props} className={cn(className, "animate-spin")} />;
}) as unknown as LucideIcon;

export interface OpenTarget {
  label: string;
  href: string;
}

function Tag({ icon, children, tone }: { icon?: ReactNode; children: ReactNode; tone?: "shared" }) {
  return (
    <span
      className={
        tone === "shared"
          ? "inline-flex items-center gap-1.5 rounded-md bg-sky-500/15 px-2 py-0.5 text-[12px] font-semibold text-sky-300"
          : "bg-muted text-muted-foreground inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-[12px] font-semibold"
      }
    >
      {icon}
      {children}
    </span>
  );
}

/** Open ▾: one library opens at once, several offer a menu. */
function OpenButton({ targets }: { targets: readonly OpenTarget[] }) {
  const t = useT();
  if (targets.length === 1) {
    return (
      <Button asChild variant="outline" size="sm">
        <Link to={targets[0]!.href}>
          <Eye aria-hidden />
          {t("web.collections.editorHeader.open")}
        </Link>
      </Button>
    );
  }
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" size="sm">
          <Eye aria-hidden />
          {t("web.collections.editorHeader.open")}
          <ChevronDown aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-48">
        {targets.map((target) => (
          <DropdownMenuItem key={target.href} asChild>
            <Link to={target.href}>{target.label}</Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/**
 * The editor's header: back link, cover, type tag, name (two lines at most),
 * meta line, and once the collection exists Open ▾ and ⋯ (Sync now for a
 * synced list, then Delete…).
 */
export function EditorHeader({
  back,
  kind,
  name,
  created,
  shared,
  posterUrl,
  meta,
  open,
  sync,
  onDelete,
}: {
  back: { label: string; href: string };
  kind: CollectionKind;
  name: string;
  created: boolean;
  shared?: boolean;
  posterUrl?: string;
  meta?: ReactNode;
  open: readonly OpenTarget[];
  /** A synced list's Sync now, disabled while a sync runs (Delete too) or until `saveFirst` changes are saved. */
  sync?: { syncing: boolean; saveFirst?: boolean; onSyncNow: () => void };
  onDelete?: () => void;
}) {
  const t = useT();
  const KindIcon = KIND_ICON[kind];
  const actions: ActionMenuAction[] = [];
  if (sync) {
    actions.push({
      key: "sync",
      label: sync.syncing ? syncingNow() : syncNow(),
      icon: sync.syncing ? Spinner : RefreshCw,
      help: sync.saveFirst && !sync.syncing ? saveBeforeSync() : undefined,
      disabled: sync.syncing || sync.saveFirst,
      onSelect: sync.onSyncNow,
    });
  }
  if (onDelete) {
    actions.push({
      key: "delete",
      label: t("web.collections.actions.delete"),
      icon: Trash2,
      destructive: true,
      group: Boolean(sync),
      // A sync moves the list's token; Delete waits for the new one.
      disabled: sync?.syncing,
      onSelect: onDelete,
    });
  }
  return (
    <div className="grid gap-4">
      <Link
        to={back.href}
        className="text-muted-foreground hover:text-foreground flex w-fit items-center gap-1 text-[14px]"
      >
        <ChevronLeft aria-hidden className="size-4" />
        {back.label}
      </Link>
      {/* The poster column only when there is a poster; Look shows the collage otherwise. */}
      <header
        className={cn(
          "grid items-end gap-x-5 gap-y-3",
          posterUrl
            ? "grid-cols-[64px_minmax(0,1fr)] sm:grid-cols-[64px_minmax(0,1fr)_auto]"
            : "grid-cols-1 sm:grid-cols-[minmax(0,1fr)_auto]",
        )}
      >
        {posterUrl ? (
          <img
            src={posterUrl}
            alt=""
            className="bg-muted aspect-[2/3] w-16 self-start rounded-[10px] object-cover"
          />
        ) : null}
        <div className="grid min-w-0 gap-1.5">
          <div className="flex flex-wrap gap-2">
            <Tag icon={<KindIcon aria-hidden className="size-3.5" />}>
              {collectionKindLabel(kind)}
            </Tag>
            {shared ? (
              <Tag tone="shared" icon={<Users aria-hidden className="size-3.5" />}>
                {t("web.collections.tags.shared")}
              </Tag>
            ) : null}
            {!created ? <Tag>{notCreatedYet()}</Tag> : null}
          </div>
          <h1 className="line-clamp-2 text-[26px] leading-[1.15] font-bold tracking-[-0.02em] break-words sm:text-[32px]">
            {name || newCollection()}
          </h1>
          {meta}
        </div>
        {created ? (
          <div className={cn("flex items-center gap-2", posterUrl && "col-span-2 sm:col-span-1")}>
            {open.length > 0 ? <OpenButton targets={open} /> : null}
            {actions.length > 0 ? (
              <ActionMenu label={t("web.collections.actions.moreActions")} items={actions} />
            ) : null}
          </div>
        ) : null}
      </header>
    </div>
  );
}
