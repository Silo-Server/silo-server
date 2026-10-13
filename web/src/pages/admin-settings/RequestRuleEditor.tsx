import { useId, useState } from "react";
import { Trash2 } from "lucide-react";

import type { RequestIntegration } from "@/api/types";
import {
  getAdminRequestRouteV2,
  isRequestEditorConflict,
  requestValidationErrors,
  type RequestRoute,
  type RequestRouteMediaType,
} from "@/api/v2/adminRequests";
import { EditorConflict } from "@/components/admin/EditorConflict";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  useCreateRequestRoute,
  useDeleteRequestRoute,
  useUpdateRequestRoute,
} from "@/hooks/queries/admin/requests";

import { FieldError, RouteDestinationEditor } from "./RequestRouteFields";
import { RuleConditionsEditor, type ConditionLookups } from "./RequestRuleConditions";
import {
  autoRuleName,
  conditionRows,
  fourKCaption,
  initialChoices,
  passThroughLabel,
  ruleBody,
  rowsToConditions,
  type ConditionRow,
  type RoutingNames,
} from "./requestRoutingModel";
import type { RequestRouterInstallation } from "./requestServerModel";

/** Everything a media type's routing editors read. */
export interface RoutingScope {
  mediaType: RequestRouteMediaType;
  /** The media type's rules in order, Everything else excluded. */
  rules: RequestRoute[];
  fallback: RequestRoute | undefined;
  /** The servers this media type can go to. */
  servers: RequestIntegration[];
  /** Every server, to name one that no longer fits. */
  allServers: RequestIntegration[];
  installations: RequestRouterInstallation[];
  lookups: ConditionLookups;
  names: RoutingNames;
  /** "Also request a 4K version of every title", as saved. */
  forceDual: boolean;
  /** The admin's language, as the Foreign-language preset uses it. */
  language: string;
}

/** Field errors an editor shows beside a control; the rest go to the top. */
function isPlacedError(key: string): boolean {
  return (
    key === "name" ||
    key === "conditions" ||
    key.startsWith("conditions.") ||
    key === "hd" ||
    key.startsWith("hd.") ||
    key === "uhd" ||
    key.startsWith("uhd.")
  );
}

/** A form-level error box: the problem's sentence and any error with no field to sit by. */
export function EditorErrors({
  message,
  fieldErrors,
}: {
  message: string | null;
  fieldErrors: Record<string, string>;
}) {
  const unplaced = Object.entries(fieldErrors).filter(([key]) => !isPlacedError(key));
  if (!message && unplaced.length === 0) return null;
  return (
    <div
      role="alert"
      className="border-destructive/40 bg-destructive/10 text-destructive space-y-1 rounded-md border px-3 py-2 text-sm"
    >
      {message ? <p>{message}</p> : null}
      {unplaced.map(([key, detail]) => (
        <p key={key}>{detail}</p>
      ))}
    </div>
  );
}

/** The rules after a rule, which decide a copy it passes on. New rules go last. */
function rulesBelow(scope: RoutingScope, source: RequestRoute | null): RequestRoute[] {
  if (!source) return [];
  const index = scope.rules.findIndex((rule) => rule.id === source.id);
  return index === -1 ? [] : scope.rules.slice(index + 1);
}

/**
 * Edits one routing rule, or builds a custom one: its name, which requests it
 * takes, and where each copy goes. Saves on its own.
 */
export function RequestRuleEditor({
  scope,
  source,
  onDone,
}: {
  scope: RoutingScope;
  source: RequestRoute | null;
  /** Called after a save or delete (with the new rule's ID after an add), and on cancel. */
  onDone: (addedId?: string) => void;
}) {
  const { mediaType } = scope;
  const [name, setName] = useState(source?.name ?? "");
  const [rows, setRows] = useState<ConditionRow[]>(() => conditionRows(source?.conditions ?? {}));
  const [choices, setChoices] = useState(() => initialChoices(source));
  const [etag, setETag] = useState(source?.etag);
  const [conflict, setConflict] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const createRoute = useCreateRequestRoute();
  const updateRoute = useUpdateRequestRoute({ inlineErrors: true });
  const deleteRoute = useDeleteRequestRoute();
  const nameId = useId();
  const whereId = useId();
  const isNew = source === null;

  const conditions = rowsToConditions(rows);
  const autoName = autoRuleName(conditions, mediaType, scope.names);
  const below = rulesBelow(scope, source);

  function touched() {
    setFieldErrors((current) => (Object.keys(current).length === 0 ? current : {}));
    setFormError(null);
  }

  function reset(route: RequestRoute) {
    setName(route.name);
    setRows(conditionRows(route.conditions));
    setChoices(initialChoices(route));
    setETag(route.etag);
  }

  async function reload() {
    if (!source) return;
    try {
      reset(await getAdminRequestRouteV2(source.id));
      setConflict(false);
      setFieldErrors({});
      setFormError(null);
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "Reload failed");
    }
  }

  function onSaveError(error: unknown) {
    if (isRequestEditorConflict(error)) setConflict(true);
    const validation = requestValidationErrors(error);
    if (validation) {
      setFieldErrors(validation.fields);
      setFormError(validation.message);
    }
  }

  function save() {
    const body = ruleBody(
      {
        // The server needs a name; a blank one saves as the description.
        name: name.trim() || autoName,
        enabled: source?.enabled ?? true,
        conditions,
        hd: choices.hd.dest,
        uhd: choices.uhd.dest,
        skipUhd: choices.uhd.skip,
      },
      isNew ? mediaType : undefined,
    );
    setFieldErrors({});
    setFormError(null);
    if (isNew) {
      createRoute.mutate(body, { onSuccess: (saved) => onDone(saved.id), onError: onSaveError });
    } else if (etag) {
      updateRoute.mutate(
        { route: { id: source.id, etag }, body },
        { onSuccess: () => onDone(), onError: onSaveError },
      );
    }
  }

  const saving = createRoute.isPending || updateRoute.isPending;

  return (
    <>
      <DialogHeader>
        <DialogTitle>{isNew ? "Custom rule" : `Edit ${source.name}`}</DialogTitle>
        <DialogDescription>
          Choose which requests this rule takes and where they go.
        </DialogDescription>
      </DialogHeader>

      <EditorErrors message={formError} fieldErrors={fieldErrors} />

      <div className="space-y-1.5">
        <label htmlFor={nameId} className="block text-sm font-semibold">
          Name
        </label>
        <Input
          id={nameId}
          value={name}
          maxLength={100}
          onChange={(event) => {
            touched();
            setName(event.target.value);
          }}
          placeholder={autoName}
          aria-invalid={Boolean(fieldErrors.name)}
          aria-describedby={`${nameId}-hint`}
          className="sm:w-1/2"
        />
        <p id={`${nameId}-hint`} className="text-muted-foreground text-xs">
          Shown in the list. Leave it blank to describe the rule automatically.
        </p>
        <FieldError>{fieldErrors.name}</FieldError>
      </div>

      <RuleConditionsEditor
        mediaType={mediaType}
        rows={rows}
        onChange={(next) => {
          touched();
          setRows(next);
        }}
        lookups={scope.lookups}
        errors={fieldErrors}
      />

      <section aria-labelledby={whereId} className="space-y-1">
        <h3 id={whereId} className="text-sm font-semibold">
          Where they go
        </h3>
        <div className="space-y-3">
          <RouteDestinationEditor
            tier="hd"
            sectionId={`${source?.id ?? `new-${mediaType}`}.hd`}
            servers={scope.servers}
            allServers={scope.allServers}
            installations={scope.installations}
            value={choices.hd}
            onChange={(hd) => {
              touched();
              setChoices((current) => ({ ...current, hd }));
            }}
            passLabel={passThroughLabel("hd", below, scope.fallback, scope.allServers)}
            errors={fieldErrors}
          />
          <RouteDestinationEditor
            tier="uhd"
            sectionId={`${source?.id ?? `new-${mediaType}`}.uhd`}
            servers={scope.servers}
            allServers={scope.allServers}
            installations={scope.installations}
            value={choices.uhd}
            onChange={(uhd) => {
              touched();
              setChoices((current) => ({ ...current, uhd }));
            }}
            allowSkip
            passLabel={passThroughLabel("uhd", below, scope.fallback, scope.allServers)}
            caption={fourKCaption(scope.forceDual)}
            errors={fieldErrors}
          />
        </div>
      </section>

      {conflict ? <EditorConflict onReload={reload} /> : null}

      <DialogFooter className="flex-wrap gap-2 sm:justify-between">
        <div>
          {!isNew ? (
            <Button
              type="button"
              variant="outline"
              className="text-destructive"
              onClick={() => setConfirmDelete(true)}
              disabled={deleteRoute.isPending || conflict || !etag}
            >
              <Trash2 aria-hidden="true" />
              Delete rule
            </Button>
          ) : null}
        </div>
        <div className="flex items-center gap-2">
          <Button type="button" variant="outline" onClick={() => onDone()}>
            Cancel
          </Button>
          <Button type="button" onClick={save} disabled={saving || conflict || (!isNew && !etag)}>
            {saving ? "Saving…" : isNew ? "Add rule" : "Save rule"}
          </Button>
        </div>
      </DialogFooter>

      {!isNew ? (
        <DeleteRuleDialog
          rule={{ ...source, etag: etag ?? source.etag }}
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          onDeleted={() => onDone()}
          onConflict={() => setConflict(true)}
        />
      ) : null}
    </>
  );
}

/** Asks before deleting a rule, and deletes it with the validator it was read with. */
export function DeleteRuleDialog({
  rule,
  open,
  onOpenChange,
  onDeleted,
  onConflict,
}: {
  rule: Pick<RequestRoute, "id" | "etag" | "name">;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted?: () => void;
  onConflict?: () => void;
}) {
  const deleteRoute = useDeleteRequestRoute();
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {rule.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            Requests it took go to the rules below it, or to Everything else.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            disabled={deleteRoute.isPending}
            onClick={(event) => {
              event.preventDefault();
              deleteRoute.mutate(
                { id: rule.id, etag: rule.etag },
                {
                  onSuccess: () => {
                    onOpenChange(false);
                    onDeleted?.();
                  },
                  onError: (error) => {
                    if (isRequestEditorConflict(error)) {
                      onOpenChange(false);
                      onConflict?.();
                    }
                  },
                },
              );
            }}
          >
            Delete
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
