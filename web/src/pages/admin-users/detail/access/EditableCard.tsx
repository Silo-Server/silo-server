import { useState, type ReactNode } from "react";
import { Lock, TriangleAlert } from "lucide-react";

import type { AccessGroup, AdminUser, Library } from "@/api/types";
import type { AdminUserEditor } from "@/api/v2/adminUsers";
import type { PolicyInheritHints } from "@/components/UserPolicyFields";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { ACCESS_CARD_TITLES, useCardEditing, type AccessCardId } from "../cardEditing";
import { DetailCard } from "../ui";
import type { InheritContext } from "./policySources";

/** What every Access & limits card reads. */
export interface AccessCardProps {
  user: AdminUser;
  /** The account with its ETag, as last read; an edit starts from it. */
  editor: AdminUserEditor | undefined;
  manageable: boolean;
  available: boolean;
  groups: AccessGroup[];
  libraries: Library[];
  /** Where the saved account inherits from. */
  ctx: InheritContext;
  /** What the saved account's inheriting fields resolve to. */
  hints: PolicyInheritHints;
}

/** What the card chrome needs from a card's draft (see useAccountCardDraft). */
export interface CardEditState {
  editing: boolean;
  changed: string[];
  conflict: boolean;
  saving: boolean;
  error: string;
  start(): void;
  cancel(): void;
  save(): Promise<boolean>;
  reload(): Promise<void>;
}

/**
 * An Access & limits card: read-only rows with an Edit button, or, while
 * editing, the edit rows with a footer that counts the changes and saves them.
 * A 412 shows a notice in the footer; Save waits until "Reload and review".
 */
export function EditableCard({
  id,
  description,
  editDescription,
  actions,
  manageable,
  available,
  canEdit = true,
  invalid = false,
  state,
  children,
}: {
  id: AccessCardId;
  description?: ReactNode;
  editDescription?: ReactNode;
  /** Links shown before Edit. */
  actions?: ReactNode;
  manageable: boolean;
  available: boolean;
  /** False while the account's editor (ETag) is not loaded. */
  canEdit?: boolean;
  /** The draft cannot be saved as it is (a field shows why). */
  invalid?: boolean;
  state: CardEditState;
  children: ReactNode;
}) {
  const { active } = useCardEditing();
  const title = ACCESS_CARD_TITLES[id];
  const otherEditing = active !== null && active.id !== id;
  const [reloading, setReloading] = useState(false);
  const count = state.changed.length;

  let control: ReactNode;
  if (state.editing) {
    control = <Badge className="border-amber-500/30 bg-amber-500/10 text-amber-300">Editing</Badge>;
  } else if (!manageable) {
    control = (
      <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
        <Lock aria-hidden="true" className="size-3" />
        View only
      </span>
    );
  } else {
    control = (
      <Button
        type="button"
        variant="outline"
        size="xs"
        aria-label={`Edit ${title}`}
        disabled={otherEditing || !available || !canEdit}
        onClick={state.start}
      >
        Edit
      </Button>
    );
  }

  const footer = state.editing ? (
    <>
      {state.conflict ? (
        <div
          role="alert"
          className="flex min-w-0 flex-1 basis-64 flex-wrap items-center gap-2 rounded-lg border border-amber-500/30 bg-amber-500/5 px-3 py-2 text-[13px] text-amber-100/90"
        >
          <TriangleAlert aria-hidden="true" className="size-4 shrink-0 text-amber-400" />
          <span className="min-w-0 flex-1 basis-48">
            Another admin changed this account after you started. Your edits are kept.
          </span>
          <Button
            type="button"
            variant="outline"
            size="xs"
            disabled={reloading}
            onClick={async () => {
              setReloading(true);
              try {
                await state.reload();
              } finally {
                setReloading(false);
              }
            }}
          >
            Reload and review
          </Button>
        </div>
      ) : (
        <span className="text-muted-foreground min-w-0 flex-1 text-xs">
          {count} unsaved {count === 1 ? "change" : "changes"}
          {count > 0 ? ` · ${state.changed.join(", ")}` : ""}
        </span>
      )}
      <div className="ml-auto flex items-center gap-2">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={state.saving}
          onClick={state.cancel}
        >
          Cancel
        </Button>
        <Button
          type="button"
          size="sm"
          disabled={count === 0 || state.saving || state.conflict || invalid}
          onClick={() => void state.save()}
        >
          {state.saving ? "Saving..." : "Save"}
        </Button>
      </div>
    </>
  ) : undefined;

  return (
    <DetailCard
      title={title}
      description={state.editing ? (editDescription ?? description) : description}
      actions={
        <>
          {actions}
          {control}
        </>
      }
      editing={state.editing}
      footer={footer}
    >
      {/* A save resets the draft when it lands, so input made meanwhile would be lost. */}
      <fieldset disabled={state.editing && state.saving} className="min-w-0">
        {children}
      </fieldset>
      {state.editing && state.error ? (
        <p role="alert" className="text-destructive px-4 pb-3 text-sm sm:px-5">
          {state.error}
        </p>
      ) : null}
    </DetailCard>
  );
}
