import { useState } from "react";

import {
  getAdminRequestRouteV2,
  isRequestEditorConflict,
  requestValidationErrors,
  type RequestRoute,
} from "@/api/v2/adminRequests";
import { EditorConflict } from "@/components/admin/EditorConflict";
import { Button } from "@/components/ui/button";
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useUpdateRequestRoute } from "@/hooks/queries/admin/requests";

import { RouteDestinationEditor } from "./RequestRouteFields";
import { EditorErrors, type RoutingScope } from "./RequestRuleEditor";
import {
  destinationDraft,
  fallbackBody,
  fourKCaption,
  type DestinationChoice,
} from "./requestRoutingModel";

function choicesOf(route: RequestRoute): { hd: DestinationChoice; uhd: DestinationChoice } {
  return {
    hd: { dest: destinationDraft(route.hd), skip: false },
    // Everything else with no 4K server makes no 4K copy.
    uhd: { dest: destinationDraft(route.uhd), skip: !route.uhd.integration_id },
  };
}

/**
 * Everything else: where a media type's requests go when no rule takes them.
 * It always needs an HD server; with no 4K server it makes no 4K copy. Saves
 * on its own, against the validator it was read with (revision zero before
 * its first save).
 */
export function RequestFallbackEditor({
  scope,
  route,
  onDone,
}: {
  scope: RoutingScope;
  route: RequestRoute;
  onDone: () => void;
}) {
  const [choices, setChoices] = useState(() => choicesOf(route));
  const [etag, setETag] = useState(route.etag);
  const [conflict, setConflict] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const updateRoute = useUpdateRequestRoute({ inlineErrors: true });
  const plural = scope.mediaType === "series" ? "series" : "movies";

  function edit(tier: "hd" | "uhd", next: DestinationChoice) {
    setFieldErrors({});
    setFormError(null);
    setChoices((current) => ({ ...current, [tier]: next }));
  }

  async function reload() {
    try {
      const latest = await getAdminRequestRouteV2(route.id);
      setChoices(choicesOf(latest));
      setETag(latest.etag);
      setConflict(false);
      setFieldErrors({});
      setFormError(null);
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "Reload failed");
    }
  }

  function save() {
    setFieldErrors({});
    setFormError(null);
    updateRoute.mutate(
      {
        route: { id: route.id, etag },
        body: fallbackBody(route, {
          hd: choices.hd.dest,
          uhd: choices.uhd.skip ? { integration_id: "", overrides: {} } : choices.uhd.dest,
        }),
      },
      {
        onSuccess: () => onDone(),
        onError: (error) => {
          if (isRequestEditorConflict(error)) setConflict(true);
          const validation = requestValidationErrors(error);
          if (validation) {
            setFieldErrors(validation.fields);
            setFormError(validation.message);
          }
        },
      },
    );
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>Everything else — {plural}</DialogTitle>
        <DialogDescription>
          Where {plural} go when no rule matches them. Leave a setting on its server default to use
          what the server itself is set to.
        </DialogDescription>
      </DialogHeader>

      <EditorErrors message={formError} fieldErrors={fieldErrors} />

      <div className="space-y-3">
        <RouteDestinationEditor
          tier="hd"
          sectionId={`${route.id}.hd`}
          servers={scope.servers}
          allServers={scope.allServers}
          installations={scope.installations}
          value={choices.hd}
          onChange={(next) => edit("hd", next)}
          errors={fieldErrors}
        />
        <RouteDestinationEditor
          tier="uhd"
          sectionId={`${route.id}.uhd`}
          servers={scope.servers}
          allServers={scope.allServers}
          installations={scope.installations}
          value={choices.uhd}
          onChange={(next) => edit("uhd", next)}
          allowSkip
          caption={fourKCaption(scope.forceDual)}
          errors={fieldErrors}
        />
      </div>

      {conflict ? <EditorConflict onReload={reload} /> : null}

      <DialogFooter className="gap-2">
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
        <Button
          type="button"
          onClick={save}
          disabled={updateRoute.isPending || conflict || !choices.hd.dest.integration_id}
        >
          {updateRoute.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </>
  );
}
