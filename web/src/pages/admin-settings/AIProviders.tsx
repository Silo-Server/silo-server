import { useState, type ReactNode } from "react";
import { toast } from "sonner";
import { Plus } from "lucide-react";

import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  ProviderPanelActions,
  ProviderTile,
  type ProviderTestState,
} from "@/components/settings/ProviderTile";
import { SecretField } from "@/components/settings/SecretField";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  useAIProviders,
  useCheckAIProvider,
  useCreateAIProvider,
  useDeleteAIProvider,
  useUpdateAIProvider,
  type AICapability,
  type AIProvider,
  type AIProviderDraft,
  type AIProviderType,
} from "@/hooks/queries/admin/aiProviders";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { AI_CAPABILITY_LABELS, hostLabel, providerOptions } from "@/lib/aiProviders";
import { cn } from "@/lib/utils";

import { SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";

// ---------------------------------------------------------------------------
// Shared labels
// ---------------------------------------------------------------------------

const ALL_CAPABILITIES: AICapability[] = ["chat", "embeddings", "speech"];

/** Jobs a provider can be used by, as `used_by` names them. */
const JOB_LABELS: Record<string, MessageKey> = {
  text: "web.admin.settings.ai.jobs.text",
  speech: "web.admin.settings.ai.jobs.speech",
  embeddings: "web.admin.settings.ai.jobs.embeddings",
};

const TYPE_MONOGRAMS: Record<AIProviderType["type"], [string, string]> = {
  openai: ["AI", "bg-emerald-500/15 text-emerald-600 dark:text-emerald-300"],
  gemini: ["GM", "bg-blue-500/15 text-blue-600 dark:text-blue-300"],
  openrouter: ["OR", "bg-violet-500/15 text-violet-600 dark:text-violet-300"],
  groq: ["GQ", "bg-orange-500/15 text-orange-600 dark:text-orange-300"],
  ollama: ["OL", "bg-muted text-foreground"],
  whisper: ["WH", "bg-slate-500/15 text-slate-600 dark:text-slate-300"],
  custom: ["{ }", "bg-muted text-muted-foreground"],
};

export function CapabilityChips({ capabilities }: { capabilities: AICapability[] }) {
  return (
    <span className="inline-flex flex-wrap gap-1">
      {capabilities.map((capability) => (
        <span
          key={capability}
          className="rounded-full border border-[var(--settings-accent-line)] bg-[var(--settings-accent-soft)] px-1.5 py-px text-[10.5px] font-medium text-[var(--settings-accent)]"
        >
          {AI_CAPABILITY_LABELS[capability]}
        </span>
      ))}
    </span>
  );
}

function usedByLabel(t: Translate, usedBy: string[]): string {
  return usedBy
    .map((job) => (JOB_LABELS[job] ? t(JOB_LABELS[job]) : job))
    .reduce((first, second) => t("web.admin.settings.ai.jobs.listJoin", { first, second }));
}

// ---------------------------------------------------------------------------
// Provider list
// ---------------------------------------------------------------------------

function draftOf(provider: AIProvider): AIProviderDraft {
  return {
    type: provider.type,
    name: provider.name,
    base_url: provider.base_url,
    capabilities: provider.capabilities,
  };
}

function draftForType(type: AIProviderType, capability: AICapability): AIProviderDraft {
  return {
    type: type.type,
    name: type.label,
    base_url: type.default_base_url,
    capabilities: type.capabilities.length > 0 ? type.capabilities : [capability],
  };
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

/**
 * Connected AI providers, each as a settings row whose panel edits it in
 * place, and the dialog that connects a new one. Provider writes save at once:
 * they are not part of the page's save bar.
 */
export function AIProvidersList({
  providers,
  types,
  leading,
  onChooseChatGPT,
}: {
  providers: AIProvider[];
  types: AIProviderType[];
  /** Rows shown before the stored providers, e.g. the ChatGPT plan. */
  leading?: ReactNode;
  /** Offers the ChatGPT plan among the types to add; it is set up elsewhere. */
  onChooseChatGPT?: () => void;
}) {
  const t = useT();
  const [expanded, setExpanded] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  return (
    <>
      {leading}
      {providers.map((provider) => (
        <ProviderRow
          key={provider.id}
          provider={provider}
          types={types}
          expanded={expanded === provider.id}
          onExpand={() => setExpanded(provider.id)}
          onCollapse={() => setExpanded(null)}
        />
      ))}
      <div className="py-3.5">
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={types.length === 0}
          onClick={() => setAdding(true)}
        >
          <Plus className="size-3.5" aria-hidden="true" />
          {t("web.admin.settings.ai.providers.add")}
        </Button>
      </div>
      <AddAIProviderDialog
        open={adding}
        onOpenChange={setAdding}
        types={types}
        onChooseChatGPT={onChooseChatGPT}
      />
    </>
  );
}

function ProviderRow({
  provider,
  types,
  expanded,
  onExpand,
  onCollapse,
}: {
  provider: AIProvider;
  types: AIProviderType[];
  expanded: boolean;
  onExpand: () => void;
  onCollapse: () => void;
}) {
  const t = useT();
  const type = types.find((candidate) => candidate.type === provider.type);
  const [monogram, monogramClass] = TYPE_MONOGRAMS[provider.type];
  const inUse = provider.used_by.length > 0;
  return (
    <ProviderTile
      name={provider.name}
      monogram={monogram}
      monogramClass={monogramClass}
      badge={<CapabilityChips capabilities={provider.capabilities} />}
      tagline={[
        type?.label,
        hostLabel(provider.base_url),
        provider.has_api_key
          ? t("web.admin.settings.ai.providers.keySaved")
          : t("web.admin.settings.ai.providers.noKey"),
      ]
        .filter(Boolean)
        .join(" · ")}
      meta={
        inUse
          ? t("web.admin.settings.ai.providers.usedBy", { jobs: usedByLabel(t, provider.used_by) })
          : t("web.admin.settings.ai.providers.notUsed")
      }
      state={expanded ? "editing" : inUse ? "connected" : "not_connected"}
      statePill={
        expanded
          ? undefined
          : inUse
            ? t("web.admin.settings.ai.tile.inUse")
            : t("web.admin.settings.ai.tile.idle")
      }
      expanded={expanded}
      primaryAction={{ label: t("web.admin.settings.ai.tile.manage"), onClick: onExpand }}
    >
      {expanded && type ? (
        <ProviderPanel
          provider={provider}
          type={type}
          initial={draftOf(provider)}
          onDone={onCollapse}
        />
      ) : null}
    </ProviderTile>
  );
}

function typeTagline(t: Translate, type: AIProviderType): string {
  if (type.capabilities.length === 0) return t("web.admin.settings.ai.providers.typeAnyEndpoint");
  return type.key_required
    ? t("web.admin.settings.ai.providers.typeApiKey")
    : t("web.admin.settings.ai.providers.typeLocal");
}

/** A selectable card in the dialog's first step. */
function TypeCard({
  label,
  tagline,
  monogram,
  capabilities,
  selected,
  onSelect,
}: {
  label: string;
  tagline: string;
  monogram: [string, string];
  capabilities: AICapability[];
  selected: boolean;
  onSelect: () => void;
}) {
  const t = useT();
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        "border-border hover:bg-accent/40 flex items-start gap-3 rounded-lg border p-3 text-left transition-colors",
        selected && "border-[var(--settings-accent)] bg-[var(--settings-accent-soft)]",
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          "grid size-8 shrink-0 place-items-center rounded-md text-xs font-bold",
          monogram[1],
        )}
      >
        {monogram[0]}
      </span>
      <span className="min-w-0">
        <span className="block text-sm font-medium">{label}</span>
        <span className="text-muted-foreground mb-1.5 block text-xs">{tagline}</span>
        {capabilities.length > 0 ? (
          <CapabilityChips capabilities={capabilities} />
        ) : (
          <span className="text-muted-foreground text-xs">
            {t("web.admin.settings.ai.providers.typeAnyCapability")}
          </span>
        )}
      </span>
    </button>
  );
}

const CHATGPT_TYPE = "chatgpt";

/**
 * Connects a provider in two steps: what kind it is, then where it is and its
 * key. `types` is never empty while the dialog is open. `capability` is what a
 * new custom endpoint is assumed to serve until the administrator says
 * otherwise.
 */
export function AddAIProviderDialog({
  open,
  onOpenChange,
  types,
  capability = "chat",
  onCreated,
  onChooseChatGPT,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  types: AIProviderType[];
  capability?: AICapability;
  onCreated?: (provider: AIProvider) => void;
  onChooseChatGPT?: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        {/* Remounting on every open starts the wizard at step one. */}
        {open ? (
          <AddAIProviderSteps
            types={types}
            capability={capability}
            onCreated={onCreated}
            onChooseChatGPT={onChooseChatGPT}
            onDone={() => onOpenChange(false)}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function AddAIProviderSteps({
  types,
  capability,
  onCreated,
  onChooseChatGPT,
  onDone,
}: {
  types: AIProviderType[];
  capability: AICapability;
  onCreated?: (provider: AIProvider) => void;
  onChooseChatGPT?: () => void;
  onDone: () => void;
}) {
  const t = useT();
  const [choice, setChoice] = useState<string>(() => types[0]?.type ?? "");
  const [step, setStep] = useState<"type" | "details">("type");
  const type = types.find((candidate) => candidate.type === choice);

  function next() {
    if (choice === CHATGPT_TYPE) {
      onDone();
      onChooseChatGPT?.();
      return;
    }
    setStep("details");
  }

  if (step === "details" && type) {
    return (
      <>
        <DialogHeader>
          <p className="text-xs font-semibold tracking-wide text-[var(--settings-accent)] uppercase">
            {t("web.admin.settings.ai.providers.step", { current: "2", total: "2" })}
          </p>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            {type.label}
            <CapabilityChips capabilities={type.capabilities} />
          </DialogTitle>
          <DialogDescription>
            {onCreated
              ? t("web.admin.settings.ai.providers.selectedOnceAdded")
              : t("web.admin.settings.ai.providers.unusedUntilSelected")}
          </DialogDescription>
        </DialogHeader>
        <ProviderPanel
          type={type}
          initial={draftForType(type, capability)}
          onDone={onDone}
          onBack={() => setStep("type")}
          onCreated={onCreated}
        />
      </>
    );
  }

  return (
    <>
      <DialogHeader>
        <p className="text-xs font-semibold tracking-wide text-[var(--settings-accent)] uppercase">
          {t("web.admin.settings.ai.providers.step", { current: "1", total: "2" })}
        </p>
        <DialogTitle>{t("web.admin.settings.ai.providers.addTitle")}</DialogTitle>
        <DialogDescription>{t("web.admin.settings.ai.providers.addDescription")}</DialogDescription>
      </DialogHeader>
      <div
        className="grid gap-2 sm:grid-cols-2"
        role="radiogroup"
        aria-label={t("web.admin.settings.ai.providers.typeGroup")}
      >
        {types.map((candidate) => (
          <TypeCard
            key={candidate.type}
            label={candidate.label}
            tagline={typeTagline(t, candidate)}
            monogram={TYPE_MONOGRAMS[candidate.type]}
            capabilities={candidate.capabilities}
            selected={choice === candidate.type}
            onSelect={() => setChoice(candidate.type)}
          />
        ))}
        {onChooseChatGPT ? (
          <TypeCard
            label={t("web.admin.settings.ai.chatgpt.plan")}
            tagline={t("web.admin.settings.ai.chatgpt.signInTagline")}
            monogram={["GP", "bg-emerald-500/10 text-emerald-700 dark:text-emerald-200"]}
            capabilities={["chat"]}
            selected={choice === CHATGPT_TYPE}
            onSelect={() => setChoice(CHATGPT_TYPE)}
          />
        ) : null}
      </div>
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          {t("common.cancel")}
        </Button>
        <Button type="button" onClick={next} disabled={choice === ""}>
          {choice === CHATGPT_TYPE
            ? t("web.admin.settings.ai.providers.setUpChatgpt")
            : t("web.admin.settings.ai.providers.continue")}
        </Button>
      </DialogFooter>
    </>
  );
}

/** Fields and actions for one provider, new when `provider` is absent. */
function ProviderPanel({
  provider,
  type,
  initial,
  onDone,
  onBack,
  onCreated,
}: {
  provider?: AIProvider;
  type: AIProviderType;
  initial: AIProviderDraft;
  onDone: () => void;
  /** Returns to the type choice of the add dialog. */
  onBack?: () => void;
  onCreated?: (provider: AIProvider) => void;
}) {
  const t = useT();
  const [draft, setDraft] = useState<AIProviderDraft>(initial);
  const [test, setTest] = useState<ProviderTestState | undefined>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const create = useCreateAIProvider();
  const update = useUpdateAIProvider();
  const remove = useDeleteAIProvider();
  const check = useCheckAIProvider();
  const custom = type.capabilities.length === 0;
  const keyCleared = draft.api_key === "" && provider?.has_api_key === true;
  const dirty =
    !provider ||
    draft.name !== initial.name ||
    draft.base_url !== initial.base_url ||
    draft.api_key !== undefined ||
    draft.capabilities.join() !== initial.capabilities.join();
  const busy = create.isPending || update.isPending || remove.isPending;
  // The server refuses to carry a saved key to another host unasked.
  const keyMustBeRetyped =
    provider?.has_api_key === true &&
    draft.api_key === undefined &&
    hostLabel(draft.base_url) !== hostLabel(provider.base_url);

  function change(next: Partial<AIProviderDraft>) {
    setDraft((current) => ({ ...current, ...next }));
    setTest(undefined);
  }

  async function runTest() {
    const started = Date.now();
    try {
      const result = await check.mutateAsync({ id: provider?.id, draft });
      setTest({
        ok: result.success,
        message: result.message,
        at: Date.now(),
        durationMs: Date.now() - started,
      });
    } catch (error) {
      setTest({
        ok: false,
        message: errorMessage(error, t("web.admin.settings.ai.providers.checkFailed")),
        at: Date.now(),
        durationMs: Date.now() - started,
      });
    }
  }

  async function save() {
    try {
      if (provider) {
        await update.mutateAsync({ id: provider.id, draft });
        toast.success(t("web.admin.settings.ai.providers.saved", { name: draft.name }));
      } else {
        const created = await create.mutateAsync(draft);
        if (onCreated) {
          onCreated(created);
          toast.success(t("web.admin.settings.ai.providers.connected", { name: draft.name }));
        } else {
          toast.success(
            t("web.admin.settings.ai.providers.connectedChooseJob", { name: draft.name }),
          );
        }
      }
      onDone();
    } catch (error) {
      toast.error(errorMessage(error, t("web.admin.settings.ai.providers.saveFailed")));
    }
  }

  async function deleteProvider() {
    if (!provider) return;
    try {
      await remove.mutateAsync(provider.id);
      toast.success(t("web.admin.settings.ai.providers.removed", { name: provider.name }));
      onDone();
    } catch (error) {
      toast.error(errorMessage(error, t("web.admin.settings.ai.providers.removeFailed")));
    } finally {
      setConfirmDelete(false);
    }
  }

  return (
    <div>
      <SettingField
        label={t("web.admin.settings.ai.providers.name")}
        value={draft.name}
        onChange={(name) => change({ name })}
      />
      <SettingField
        label={t("web.admin.settings.ai.providers.baseUrl")}
        value={draft.base_url}
        onChange={(base_url) => change({ base_url })}
        hint={type.default_base_url || "https://llm.example.net/v1"}
        description={
          custom
            ? t("web.admin.settings.ai.providers.baseUrlCustom")
            : type.type === "ollama" || type.type === "whisper"
              ? t("web.admin.settings.ai.providers.baseUrlLocal")
              : t("web.admin.settings.ai.providers.baseUrlProxy")
        }
        status={
          keyMustBeRetyped ? (
            <SettingFieldStatus tone="warn">
              {t("web.admin.settings.ai.providers.retypeKey")}
            </SettingFieldStatus>
          ) : undefined
        }
      />
      <SecretField
        label={t("web.admin.settings.ai.providers.apiKey")}
        value={draft.api_key ?? ""}
        configured={provider?.has_api_key === true}
        onChange={(api_key) => change({ api_key })}
        onKeep={() => change({ api_key: undefined })}
        onClear={provider?.has_api_key ? () => change({ api_key: "" }) : undefined}
        cleared={keyCleared}
        hint={type.key_required ? undefined : t("web.admin.settings.ai.providers.apiKeyOptional")}
      />
      {custom ? (
        <SettingFieldRow
          label={t("web.admin.settings.ai.providers.serves")}
          description={t("web.admin.settings.ai.providers.servesDescription")}
        >
          <div
            className="flex flex-wrap gap-2"
            role="group"
            aria-label={t("web.admin.settings.ai.providers.serves")}
          >
            {ALL_CAPABILITIES.map((capability) => {
              const on = draft.capabilities.includes(capability);
              return (
                <button
                  key={capability}
                  type="button"
                  aria-pressed={on}
                  onClick={() =>
                    change({
                      capabilities: on
                        ? draft.capabilities.filter((item) => item !== capability)
                        : ALL_CAPABILITIES.filter(
                            (item) => item === capability || draft.capabilities.includes(item),
                          ),
                    })
                  }
                  className={cn(
                    "border-border hover:bg-accent rounded-md border px-3 py-1.5 text-xs transition-colors",
                    on && "border-primary bg-primary/5 text-primary",
                  )}
                >
                  {AI_CAPABILITY_LABELS[capability]}
                </button>
              );
            })}
          </div>
        </SettingFieldRow>
      ) : null}
      <ProviderPanelActions test={test}>
        {onBack ? (
          <Button type="button" size="sm" variant="outline" onClick={onBack} disabled={busy}>
            {t("common.back")}
          </Button>
        ) : null}
        {provider ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="text-destructive"
            disabled={busy || provider.used_by.length > 0}
            title={
              provider.used_by.length > 0
                ? t("web.admin.settings.ai.providers.removeBlocked", {
                    jobs: usedByLabel(t, provider.used_by),
                  })
                : undefined
            }
            onClick={() => setConfirmDelete(true)}
          >
            {t("web.admin.settings.ai.providers.remove")}
          </Button>
        ) : null}
        <Button
          type="button"
          size="sm"
          variant="secondary"
          onClick={() => void runTest()}
          disabled={check.isPending || draft.base_url.trim() === ""}
        >
          {check.isPending
            ? t("web.admin.settings.ai.providers.testing")
            : t("web.admin.settings.ai.providers.test")}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onDone} disabled={busy}>
          {dirty ? t("common.cancel") : t("web.admin.settings.ai.tile.close")}
        </Button>
        {dirty ? (
          <Button type="button" size="sm" onClick={() => void save()} disabled={busy}>
            {provider ? t("common.save") : t("web.admin.settings.ai.providers.addSubmit")}
          </Button>
        ) : null}
      </ProviderPanelActions>
      {provider && provider.used_by.length > 0 ? (
        <p className="text-muted-foreground mt-2 text-xs">
          {t("web.admin.settings.ai.providers.usedByNote", {
            jobs: usedByLabel(t, provider.used_by),
          })}
        </p>
      ) : null}
      {provider ? (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={t("web.admin.settings.ai.providers.removeTitle", { name: provider.name })}
          description={t("web.admin.settings.ai.providers.removeDescription")}
          confirmLabel={t("web.admin.settings.ai.providers.removeConfirm")}
          variant="destructive"
          isPending={remove.isPending}
          onConfirm={() => void deleteProvider()}
        />
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Embedding provider
// ---------------------------------------------------------------------------

const ADD_PROVIDER = "add";
/** Releases the embedding provider, so it can be removed once nothing uses it. */
const NO_EMBEDDING_PROVIDER = "none";

/**
 * Chooses the provider recommendation embeddings use, and connects a new one
 * in place. Providers that cannot serve embeddings stay listed, disabled, so
 * an administrator sees why a connected provider is not offered.
 */
export function EmbeddingProviderField({
  value,
  onChange,
  disabled,
  restartRequired,
}: {
  value: string;
  onChange: (id: string) => void;
  disabled?: boolean;
  restartRequired?: boolean;
}) {
  const t = useT();
  const list = useAIProviders();
  const [adding, setAdding] = useState(false);
  const providers = list.data?.providers ?? [];
  const types = (list.data?.types ?? []).filter(
    (type) => type.capabilities.length === 0 || type.capabilities.includes("embeddings"),
  );
  const unable = providers
    .filter((provider) => !provider.capabilities.includes("embeddings"))
    .map((provider) => ({
      value: provider.id,
      label: t("web.admin.settings.ai.embeddingProvider.noModels", { name: provider.name }),
      disabled: true,
    }));
  return (
    <>
      <SettingField
        label={t("web.admin.settings.ai.embeddingProvider.label")}
        type="select"
        value={value || NO_EMBEDDING_PROVIDER}
        disabled={disabled || list.isLoading}
        restartRequired={restartRequired}
        description={t("web.admin.settings.ai.embeddingProvider.description")}
        onChange={(next) => {
          if (next === ADD_PROVIDER) setAdding(true);
          else onChange(next === NO_EMBEDDING_PROVIDER ? "" : next);
        }}
        options={[
          { value: NO_EMBEDDING_PROVIDER, label: t("web.admin.settings.ai.provider.none") },
          ...providerOptions(providers, "embeddings", value),
          ...unable.filter((option) => option.value !== value),
          ...(types.length > 0
            ? [
                {
                  value: ADD_PROVIDER,
                  label: t("web.admin.settings.ai.embeddingProvider.connect"),
                },
              ]
            : []),
        ]}
      />
      <AddAIProviderDialog
        open={adding}
        onOpenChange={setAdding}
        types={types}
        capability="embeddings"
        onCreated={(provider) => onChange(provider.id)}
      />
    </>
  );
}
