import { useState } from "react";

import { isRequestEditorConflict, requestValidationErrors } from "@/api/v2/adminRequests";
import { Button } from "@/components/ui/button";
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useCreateRequestRoute } from "@/hooks/queries/admin/requests";
import { ChoiceCard } from "@/pages/admin/autoscan/ChoiceCard";

import { RouteDestinationEditor } from "./RequestRouteFields";
import { EditorErrors, type RoutingScope } from "./RequestRuleEditor";
import { RuleConditionsEditor } from "./RequestRuleConditions";
import {
  conditionRows,
  fourKCaption,
  initialChoices,
  offersAnimeSeriesType,
  passThroughLabel,
  ruleBody,
  rowsToConditions,
  serverOverrideFields,
  type ConditionRow,
  type DestinationChoice,
  type Tier,
} from "./requestRoutingModel";
import {
  presetsInUse,
  RULE_PRESET_IDS,
  rulePreset,
  type RulePreset,
  type RulePresetId,
} from "./requestRoutingPresets";
import { serverKind } from "./requestServerModel";

/**
 * "Add a rule": pick a common rule (or a custom one), then, for a common
 * rule, say where its requests go.
 */
export function AddRuleFlow({
  scope,
  onCustom,
  onDone,
}: {
  scope: RoutingScope;
  /** The admin chose to build a rule from scratch. */
  onCustom: () => void;
  /** Called after an add (with the new rule's ID) and on cancel. */
  onDone: (addedId?: string) => void;
}) {
  const [picked, setPicked] = useState<RulePresetId | null>(null);
  if (picked) {
    return (
      <PresetStep
        // A new pick starts a new form.
        key={picked}
        scope={scope}
        preset={rulePreset(picked, scope.mediaType, scope.language)}
        onBack={() => setPicked(null)}
        onDone={onDone}
      />
    );
  }
  const inUse = presetsInUse(scope.rules, scope.mediaType, scope.language);
  return (
    <>
      <DialogHeader>
        <DialogTitle>Add a rule</DialogTitle>
        <DialogDescription>Start from a common rule or build your own.</DialogDescription>
      </DialogHeader>
      <div className="grid gap-2 sm:grid-cols-2">
        {RULE_PRESET_IDS.map((id) => {
          const preset = rulePreset(id, scope.mediaType, scope.language);
          return (
            <ChoiceCard
              key={id}
              title={preset.name}
              description={preset.description}
              badge={inUse.has(id) ? "Already added" : undefined}
              selected={false}
              onSelect={() => setPicked(id)}
            />
          );
        })}
        <ChoiceCard
          title="Custom rule"
          description="Choose your own conditions."
          selected={false}
          onSelect={onCustom}
        />
      </div>
      <DialogFooter>
        <Button type="button" variant="outline" onClick={() => onDone()}>
          Cancel
        </Button>
      </DialogFooter>
    </>
  );
}

/**
 * A common rule's second step: where its requests go. Its conditions are
 * set, and can be changed. The Anime rule sets Sonarr's series type to Anime
 * on a Sonarr server that offers it.
 */
function PresetStep({
  scope,
  preset,
  onBack,
  onDone,
}: {
  scope: RoutingScope;
  preset: RulePreset;
  onBack: () => void;
  onDone: (addedId?: string) => void;
}) {
  const { mediaType } = scope;
  const [rows, setRows] = useState<ConditionRow[]>(() => conditionRows(preset.conditions));
  const [changing, setChanging] = useState(false);
  const [choices, setChoices] = useState(() => initialChoices(null));
  const [setByPreset, setSetByPreset] = useState<Record<Tier, boolean>>({ hd: false, uhd: false });
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const createRoute = useCreateRequestRoute();

  /** Anime on a Sonarr server that has the Anime series type: set it. */
  function withPresetOverrides(tier: Tier, next: DestinationChoice): DestinationChoice {
    const previous = choices[tier].dest.integration_id;
    const server = scope.allServers.find((candidate) => candidate.id === next.dest.integration_id);
    if (preset.id !== "anime" || mediaType !== "series" || !server) return next;
    if (next.dest.integration_id === previous) return next;
    if (serverKind(server) !== "sonarr") return next;
    if (!offersAnimeSeriesType(serverOverrideFields(server, scope.installations))) return next;
    setSetByPreset((current) => ({ ...current, [tier]: true }));
    return {
      ...next,
      dest: { ...next.dest, overrides: { ...next.dest.overrides, series_type: "anime" } },
    };
  }

  function choose(tier: Tier, next: DestinationChoice) {
    setFieldErrors({});
    setFormError(null);
    if (next.dest.integration_id !== choices[tier].dest.integration_id) {
      setSetByPreset((current) => ({ ...current, [tier]: false }));
    }
    const value = withPresetOverrides(tier, next);
    setChoices((current) => ({ ...current, [tier]: value }));
  }

  function note(tier: Tier) {
    return setByPreset[tier] && choices[tier].dest.overrides.series_type === "anime"
      ? "Series type: Anime (set by the Anime preset)"
      : undefined;
  }

  function add() {
    setFieldErrors({});
    setFormError(null);
    createRoute.mutate(
      ruleBody(
        {
          name: preset.name,
          enabled: true,
          conditions: rowsToConditions(rows),
          hd: choices.hd.dest,
          uhd: choices.uhd.dest,
          skipUhd: choices.uhd.skip,
        },
        mediaType,
      ),
      {
        onSuccess: (saved) => onDone(saved.id),
        onError: (error) => {
          if (isRequestEditorConflict(error)) return;
          const validation = requestValidationErrors(error);
          if (validation) {
            setFieldErrors(validation.fields);
            setFormError(validation.message);
            // A condition the server refused stays in view once the error
            // clears, so the admin can keep fixing it.
            if (
              Object.keys(validation.fields).some(
                (key) => key === "conditions" || key.startsWith("conditions."),
              )
            ) {
              setChanging(true);
            }
          }
        },
      },
    );
  }

  const conditionErrors = Object.keys(fieldErrors).some(
    (key) => key === "conditions" || key.startsWith("conditions."),
  );

  return (
    <>
      <DialogHeader>
        <DialogTitle>Where should {preset.noun} go?</DialogTitle>
        {!changing && !conditionErrors ? (
          <DialogDescription>
            Matches: {preset.matches}.{" "}
            <Button
              type="button"
              variant="link"
              size="xs"
              className="h-auto p-0 align-baseline"
              onClick={() => setChanging(true)}
            >
              Change
            </Button>
          </DialogDescription>
        ) : (
          <DialogDescription>
            Change which requests the rule takes, then where they go.
          </DialogDescription>
        )}
      </DialogHeader>

      <EditorErrors message={formError} fieldErrors={fieldErrors} />

      {changing || conditionErrors ? (
        <RuleConditionsEditor
          mediaType={mediaType}
          rows={rows}
          onChange={(next) => {
            setFieldErrors({});
            setFormError(null);
            setRows(next);
          }}
          lookups={scope.lookups}
          errors={fieldErrors}
        />
      ) : null}

      <div className="space-y-3">
        <RouteDestinationEditor
          tier="hd"
          sectionId={`preset-${preset.id}-${mediaType}.hd`}
          servers={scope.servers}
          allServers={scope.allServers}
          installations={scope.installations}
          value={choices.hd}
          onChange={(next) => choose("hd", next)}
          note={note("hd")}
          errors={fieldErrors}
        />
        <RouteDestinationEditor
          tier="uhd"
          sectionId={`preset-${preset.id}-${mediaType}.uhd`}
          servers={scope.servers}
          allServers={scope.allServers}
          installations={scope.installations}
          value={choices.uhd}
          onChange={(next) => choose("uhd", next)}
          allowSkip
          passLabel={passThroughLabel("uhd", [], scope.fallback, scope.allServers)}
          caption={fourKCaption(scope.forceDual)}
          note={note("uhd")}
          errors={fieldErrors}
        />
      </div>

      <DialogFooter className="gap-2 sm:justify-between">
        <Button type="button" variant="outline" onClick={onBack}>
          Back
        </Button>
        <Button
          type="button"
          onClick={add}
          disabled={createRoute.isPending || !choices.hd.dest.integration_id}
        >
          {createRoute.isPending ? "Adding…" : "Add rule"}
        </Button>
      </DialogFooter>
    </>
  );
}
