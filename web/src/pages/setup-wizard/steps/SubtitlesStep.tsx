import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import {
  captureProviderEditIntent,
  getProviderEditor,
  providerIntentActive,
  type ProviderEditor,
} from "@/api/v2/adminSubtitleProviderConfiguration";
import type { components } from "@/api/v2/schema";
import { SecretField } from "@/components/settings/SecretField";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  useSubtitleProviders,
  useTestSubtitleProvider,
  useUpdateSubtitleProvider,
} from "@/hooks/queries/admin/subtitles";
import { useT, type MessageKey } from "@/i18n";
import { sortSubtitleProviders } from "@/lib/subtitleProviders";
import { SettingField, SettingFieldRow } from "@/pages/admin-settings/SettingField";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSummary } from "../useStep";
import { useWizardContext } from "../WizardContext";

type SubtitleProviderConfig = components["schemas"]["AdminSubtitleProvider"];

const PROVIDER_META: Record<string, { name: string; description: MessageKey }> = {
  opensubtitles: {
    name: "OpenSubtitles",
    description: "web.setup.subtitles.openSubtitlesDescription",
  },
  subdl: {
    name: "SubDL",
    description: "web.setup.subtitles.subdlDescription",
  },
  subsource: {
    name: "SubSource",
    description: "web.setup.subtitles.subsourceDescription",
  },
};

interface ProviderDraft {
  editor: ProviderEditor | null;
  loading: boolean;
  loadFailed: boolean;
  enabled: boolean;
  apiKey: string;
  username: string;
  password: string;
  dirty: boolean;
}

function draftFor(config: SubtitleProviderConfig): ProviderDraft {
  return {
    editor: null,
    // A row starts out waiting for its editor; the load clears this.
    loading: true,
    loadFailed: false,
    enabled: config.enabled,
    apiKey: "",
    username: "",
    password: "",
    dirty: false,
  };
}

function providerBody(name: string, draft: ProviderDraft) {
  return name === "opensubtitles"
    ? { enabled: draft.enabled, username: draft.username, password: draft.password }
    : { enabled: draft.enabled, api_key: draft.apiKey };
}

/**
 * One subtitle provider: a switch on the row, credentials underneath while it
 * is on. Saving happens once, on Continue, for every provider that changed.
 */
function ProviderRow({
  config,
  draft,
  onChange,
  disabled,
}: {
  config: SubtitleProviderConfig;
  draft: ProviderDraft;
  onChange: (next: ProviderDraft) => void;
  disabled: boolean;
}) {
  const t = useT();
  const name = config.provider_name;
  const meta = PROVIDER_META[name];
  const displayName = meta?.name ?? name;
  const isOpenSubtitles = name === "opensubtitles";
  const canonical = draft.editor?.body ?? config;
  const hasCredentials = isOpenSubtitles ? canonical.has_credentials : canonical.has_api_key;
  const test = useTestSubtitleProvider();
  // The result is kept with the exact credentials it tested and shown only
  // while the draft still matches them, so editing a field clears it and a
  // late response for an older draft is never shown against the new one.
  const [tested, setTested] = useState<{
    signature: string;
    result: { success: boolean; error?: string };
  } | null>(null);
  const signature = JSON.stringify(providerBody(name, draft));
  const testResult = tested?.signature === signature ? tested.result : null;

  function runTest() {
    const body = providerBody(name, draft);
    const testedSignature = JSON.stringify(body);
    test.mutate(
      { provider: name, config: body },
      {
        onSuccess: (result) =>
          setTested({
            signature: testedSignature,
            result: { success: result.success, error: result.error },
          }),
        onError: (err) =>
          setTested({
            signature: testedSignature,
            result: {
              success: false,
              error: err instanceof Error ? err.message : t("web.setup.subtitles.testFailed"),
            },
          }),
      },
    );
  }

  const rowDisabled = disabled || draft.loading || draft.loadFailed;

  return (
    <>
      <SettingFieldRow
        label={displayName}
        description={
          draft.loadFailed ? t("web.setup.subtitles.loadFailed") : meta ? t(meta.description) : ""
        }
        status={
          hasCredentials && !draft.dirty ? (
            <span className="text-muted-foreground text-xs">
              {t("web.setup.subtitles.credentialsSaved")}
            </span>
          ) : undefined
        }
      >
        <Switch
          checked={draft.enabled}
          aria-label={t("web.setup.subtitles.enableLabel", { provider: displayName })}
          disabled={rowDisabled}
          onCheckedChange={(v) => onChange({ ...draft, enabled: v, dirty: true })}
        />
      </SettingFieldRow>
      {draft.enabled && !draft.loadFailed ? (
        <div className="setup-provider-fields">
          {isOpenSubtitles ? (
            <>
              <SettingField
                label={t("web.auth.username")}
                description={hasCredentials ? t("web.setup.subtitles.usernameKeep") : undefined}
                value={draft.username}
                onChange={(v) => onChange({ ...draft, username: v, dirty: true })}
                disabled={rowDisabled}
              />
              <SecretField
                label={t("web.auth.password")}
                value={draft.password}
                configured={hasCredentials}
                onChange={(v) => onChange({ ...draft, password: v, dirty: true })}
                disabled={rowDisabled}
              />
            </>
          ) : (
            <SecretField
              label={t("web.setup.subtitles.apiKey")}
              value={draft.apiKey}
              configured={hasCredentials}
              onChange={(v) => onChange({ ...draft, apiKey: v, dirty: true })}
              disabled={rowDisabled}
            />
          )}
          <div className="setup-provider-actions">
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={runTest}
              disabled={rowDisabled || test.isPending}
            >
              {test.isPending ? (
                <>
                  <Loader2 className="me-1.5 size-3 animate-spin" />
                  {t("web.setup.subtitles.testing")}
                </>
              ) : (
                t("web.setup.subtitles.test")
              )}
            </Button>
            {testResult !== null ? (
              <span className={testResult.success ? "text-green-500" : "text-red-400"}>
                {testResult.success
                  ? t("web.setup.wizard.connected")
                  : (testResult.error ?? t("web.setup.subtitles.failed"))}
              </span>
            ) : null}
          </div>
        </div>
      ) : null}
    </>
  );
}

export function SubtitlesStep() {
  const { markDone } = useWizardContext();
  const t = useT();
  const providers = useSubtitleProviders();
  const updateProvider = useUpdateSubtitleProvider();
  const [submitting, setSubmitting] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, ProviderDraft>>({});
  const loaded = useRef(new Set<string>());

  const providerList = useMemo(
    () => sortSubtitleProviders(providers.data?.providers ?? []),
    [providers.data],
  );

  // Editing a provider needs its strong validator, which only the per-provider
  // read returns. Each one is loaded once when the list arrives so the switch
  // can be flipped straight away, and reloaded after a failed save so a retry
  // never carries a stale validator. A row whose editor fails to load stays
  // read-only.
  const loadEditor = useCallback(
    (config: SubtitleProviderConfig, isCancelled: () => boolean) => {
      const name = config.provider_name;
      let intent;
      try {
        intent = captureProviderEditIntent(name, providers.scope);
      } catch {
        loaded.current.delete(name);
        setDrafts((prev) => ({
          ...prev,
          [name]: { ...(prev[name] ?? draftFor(config)), loading: false, loadFailed: true },
        }));
        return;
      }
      getProviderEditor(intent)
        .then((editor) => {
          if (isCancelled() || !providerIntentActive(editor.intent)) {
            // Let the next list refresh issue a replacement read.
            loaded.current.delete(name);
            return;
          }
          setDrafts((prev) => {
            const current = prev[name] ?? draftFor(config);
            return {
              ...prev,
              [name]: {
                ...current,
                editor,
                loading: false,
                enabled: current.dirty ? current.enabled : editor.body.enabled,
              },
            };
          });
        })
        .catch(() => {
          if (isCancelled()) {
            loaded.current.delete(name);
            return;
          }
          setDrafts((prev) => ({
            ...prev,
            [name]: { ...(prev[name] ?? draftFor(config)), loading: false, loadFailed: true },
          }));
        });
    },
    [providers.scope],
  );

  useEffect(() => {
    if (providerList.length === 0) return;
    let cancelled = false;
    for (const config of providerList) {
      // A refetched list (after a save) must not re-read editors that are
      // already loaded or in flight.
      if (loaded.current.has(config.provider_name)) continue;
      loaded.current.add(config.provider_name);
      loadEditor(config, () => cancelled);
    }
    return () => {
      cancelled = true;
    };
  }, [providerList, loadEditor]);

  const enabledProviders = providerList.filter((p) => drafts[p.provider_name]?.enabled).length;

  useStepSummary(
    "subtitles",
    enabledProviders > 0
      ? t("web.setup.subtitles.summary", {
          count: enabledProviders,
          shown: String(enabledProviders),
        })
      : undefined,
  );

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const changed = providerList.filter((p) => {
      const draft = drafts[p.provider_name];
      return draft?.dirty && draft.editor;
    });
    if (changed.length === 0) {
      markDone("subtitles");
      return;
    }
    setSubmitting(true);
    // Each provider is its own resource with its own validator, so the saves
    // are independent and run together. A failed one keeps its draft but
    // reloads its validator, so the retry cannot send a stale If-Match.
    const results = await Promise.allSettled(
      changed.map(async (config) => {
        const name = config.provider_name;
        const draft = drafts[name]!;
        await updateProvider.mutateAsync({
          editor: draft.editor!,
          config: providerBody(name, draft),
        });
        // The save advanced the provider's revision, so the editor held here
        // is stale. Drop it and read a fresh one in case the step is reopened.
        setDrafts((prev) => ({
          ...prev,
          [name]: {
            ...prev[name]!,
            editor: null,
            loading: true,
            dirty: false,
            apiKey: "",
            username: "",
            password: "",
          },
        }));
        loadEditor(config, () => false);
      }),
    );
    setSubmitting(false);
    const failures = results.flatMap((result, i) =>
      result.status === "rejected" ? [{ config: changed[i]!, reason: result.reason }] : [],
    );
    if (failures.length === 0) {
      markDone("subtitles");
      return;
    }
    for (const failure of failures) {
      setDrafts((prev) => ({
        ...prev,
        [failure.config.provider_name]: {
          ...prev[failure.config.provider_name]!,
          editor: null,
          loading: true,
          loadFailed: false,
        },
      }));
      loadEditor(failure.config, () => false);
    }
    const reason = failures[0]!.reason;
    toast.error(reason instanceof Error ? reason.message : t("web.setup.subtitles.saveFailed"));
  }

  if (providers.isLoading) return <StepSkeleton rows={3} />;

  return (
    <StepFrame
      title={t("web.setup.subtitles.title")}
      lede={t("web.setup.subtitles.lede")}
      onSubmit={handleSubmit}
      busy={submitting}
      onSkip={() => markDone("subtitles")}
      footnote={t("web.setup.subtitles.footnote")}
    >
      <StepSection>
        {providers.isError ? (
          <div className="py-4">
            <p className="text-sm font-medium">{t("web.setup.subtitles.listLoadFailed")}</p>
            <p className="text-muted-foreground mt-1 text-xs">
              {providers.error instanceof Error
                ? providers.error.message
                : t("web.setup.subtitles.noAnswer")}
            </p>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              className="mt-3"
              onClick={() => void providers.refetch()}
            >
              {t("common.tryAgain")}
            </Button>
          </div>
        ) : providerList.length === 0 ? (
          <p className="text-muted-foreground py-4 text-sm">{t("web.setup.subtitles.none")}</p>
        ) : (
          providerList.map((config) => (
            <ProviderRow
              key={`${providers.scope}:${config.provider_name}`}
              config={config}
              draft={drafts[config.provider_name] ?? draftFor(config)}
              onChange={(next) => setDrafts((prev) => ({ ...prev, [config.provider_name]: next }))}
              disabled={submitting}
            />
          ))
        )}
      </StepSection>
    </StepFrame>
  );
}
