import { useId, useState, type ReactNode } from "react";
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
import { Input } from "@/components/ui/input";
import {
  useAIProviderModels,
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
import { AI_CAPABILITY_LABELS, hostLabel, providerOptions } from "@/lib/aiProviders";
import { cn } from "@/lib/utils";

import {
  SETTINGS_CONTROL_WIDTH,
  SettingField,
  SettingFieldRow,
  SettingFieldStatus,
} from "./SettingField";

// ---------------------------------------------------------------------------
// Shared labels
// ---------------------------------------------------------------------------

const ALL_CAPABILITIES: AICapability[] = ["chat", "embeddings", "speech"];

/** Jobs a provider can be used by, as `used_by` names them. */
const JOB_LABELS: Record<string, string> = {
  text: "Text model",
  speech: "Speech-to-text",
  embeddings: "Embeddings",
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

function usedByLabel(usedBy: string[]): string {
  return usedBy.map((job) => JOB_LABELS[job] ?? job).join(" and ");
}

// ---------------------------------------------------------------------------
// Model field
// ---------------------------------------------------------------------------

/**
 * A model name with the selected provider's listed models as suggestions. The
 * list is a convenience: providers that do not list models, or list them
 * without saying which are which, still take any typed name.
 */
export function ModelField({
  label,
  description,
  providerID,
  kind,
  value,
  onChange,
  onCommit,
  placeholder,
}: {
  label: string;
  description?: ReactNode;
  providerID: string;
  kind: AICapability;
  value: string;
  onChange: (value: string) => void;
  /** Called when the field loses focus, for pages that save each field. */
  onCommit?: () => void;
  placeholder?: string;
}) {
  const inputID = useId();
  const listID = useId();
  const models = useAIProviderModels(providerID);
  const suggestions = (models.data?.models ?? []).filter((model) => model.kind === kind);
  const status =
    providerID === "" ? undefined : models.isError ? (
      <span className="text-muted-foreground text-xs">
        Could not load the provider&rsquo;s models; type the name.
      </span>
    ) : suggestions.length > 0 ? (
      <span className="text-muted-foreground text-xs">
        {suggestions.length} suggested by the provider
      </span>
    ) : undefined;
  return (
    <SettingFieldRow label={label} htmlFor={inputID} description={description} status={status}>
      <Input
        id={inputID}
        list={listID}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onBlur={onCommit}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        className={cn(SETTINGS_CONTROL_WIDTH, "font-mono text-[13px]")}
      />
      <datalist id={listID}>
        {suggestions.map((model) => (
          <option key={model.id} value={model.id} />
        ))}
      </datalist>
    </SettingFieldRow>
  );
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
          Add provider
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
        provider.has_api_key ? "key saved" : "no key",
      ]
        .filter(Boolean)
        .join(" · ")}
      meta={inUse ? `Used by ${usedByLabel(provider.used_by)}` : "Not used by any job"}
      state={expanded ? "editing" : inUse ? "connected" : "not_connected"}
      statePill={expanded ? undefined : inUse ? "In use" : "Idle"}
      expanded={expanded}
      primaryAction={{ label: "Manage", onClick: onExpand }}
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

function typeTagline(type: AIProviderType): string {
  if (type.capabilities.length === 0) return "Any OpenAI-compatible endpoint";
  return type.key_required ? "API key" : "Local, no key";
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
          <span className="text-muted-foreground text-xs">You choose what it serves</span>
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
            Step 2 of 2
          </p>
          <DialogTitle className="flex flex-wrap items-center gap-2">
            {type.label}
            <CapabilityChips capabilities={type.capabilities} />
          </DialogTitle>
          <DialogDescription>
            {onCreated
              ? "Once added, it is selected here."
              : "Nothing uses the provider until a job selects it under Models."}
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
          Step 1 of 2
        </p>
        <DialogTitle>Add an AI provider</DialogTitle>
        <DialogDescription>
          Connect each provider once. Its key then serves every job that chooses it.
        </DialogDescription>
      </DialogHeader>
      <div className="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-label="Provider type">
        {types.map((candidate) => (
          <TypeCard
            key={candidate.type}
            label={candidate.label}
            tagline={typeTagline(candidate)}
            monogram={TYPE_MONOGRAMS[candidate.type]}
            capabilities={candidate.capabilities}
            selected={choice === candidate.type}
            onSelect={() => setChoice(candidate.type)}
          />
        ))}
        {onChooseChatGPT ? (
          <TypeCard
            label="ChatGPT plan"
            tagline="Sign in with a ChatGPT account"
            monogram={["GP", "bg-emerald-500/10 text-emerald-700 dark:text-emerald-200"]}
            capabilities={["chat"]}
            selected={choice === CHATGPT_TYPE}
            onSelect={() => setChoice(CHATGPT_TYPE)}
          />
        ) : null}
      </div>
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
        <Button type="button" onClick={next} disabled={choice === ""}>
          {choice === CHATGPT_TYPE ? "Set up ChatGPT plan" : "Continue"}
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
        message: errorMessage(error, "The check could not run."),
        at: Date.now(),
        durationMs: Date.now() - started,
      });
    }
  }

  async function save() {
    try {
      if (provider) {
        await update.mutateAsync({ id: provider.id, draft });
        toast.success(`${draft.name} saved.`);
      } else {
        const created = await create.mutateAsync(draft);
        if (onCreated) {
          onCreated(created);
          toast.success(`${draft.name} connected.`);
        } else {
          toast.success(`${draft.name} connected. Choose it for a job under Models.`);
        }
      }
      onDone();
    } catch (error) {
      toast.error(errorMessage(error, "The provider could not be saved."));
    }
  }

  async function deleteProvider() {
    if (!provider) return;
    try {
      await remove.mutateAsync(provider.id);
      toast.success(`${provider.name} removed.`);
      onDone();
    } catch (error) {
      toast.error(errorMessage(error, "The provider could not be removed."));
    } finally {
      setConfirmDelete(false);
    }
  }

  return (
    <div>
      <SettingField label="Name" value={draft.name} onChange={(name) => change({ name })} />
      <SettingField
        label="Base URL"
        value={draft.base_url}
        onChange={(base_url) => change({ base_url })}
        hint={type.default_base_url || "https://llm.example.net/v1"}
        description={
          custom
            ? "Any endpoint that speaks the OpenAI API."
            : type.type === "ollama" || type.type === "whisper"
              ? "Use a host the Silo container can reach; localhost is Silo itself."
              : "Change only for a proxy or gateway."
        }
        status={
          keyMustBeRetyped ? (
            <SettingFieldStatus tone="warn">
              Enter the API key again for a new address, or clear it.
            </SettingFieldStatus>
          ) : undefined
        }
      />
      <SecretField
        label="API key"
        value={draft.api_key ?? ""}
        configured={provider?.has_api_key === true}
        onChange={(api_key) => change({ api_key })}
        onKeep={() => change({ api_key: undefined })}
        onClear={provider?.has_api_key ? () => change({ api_key: "" }) : undefined}
        cleared={keyCleared}
        hint={type.key_required ? undefined : "Empty for a local endpoint that needs none."}
      />
      {custom ? (
        <SettingFieldRow label="Serves" description="What this endpoint can answer.">
          <div className="flex flex-wrap gap-2" role="group" aria-label="Serves">
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
            Back
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
                ? `Used by ${usedByLabel(provider.used_by)}; choose another provider there first.`
                : undefined
            }
            onClick={() => setConfirmDelete(true)}
          >
            Remove
          </Button>
        ) : null}
        <Button
          type="button"
          size="sm"
          variant="secondary"
          onClick={() => void runTest()}
          disabled={check.isPending || draft.base_url.trim() === ""}
        >
          {check.isPending ? "Testing..." : "Test"}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onDone} disabled={busy}>
          {dirty ? "Cancel" : "Close"}
        </Button>
        {dirty ? (
          <Button type="button" size="sm" onClick={() => void save()} disabled={busy}>
            {provider ? "Save" : "Add provider"}
          </Button>
        ) : null}
      </ProviderPanelActions>
      {provider && provider.used_by.length > 0 ? (
        <p className="text-muted-foreground mt-2 text-xs">
          Used by {usedByLabel(provider.used_by)}. A new key applies to it as soon as it is saved.
        </p>
      ) : null}
      {provider ? (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={`Remove ${provider.name}?`}
          description="Silo discards the saved key. Connect the provider again to use it."
          confirmLabel="Remove provider"
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
      label: `${provider.name} (no embedding models)`,
      disabled: true,
    }));
  return (
    <>
      <SettingField
        label="Embedding provider"
        type="select"
        value={value || NO_EMBEDDING_PROVIDER}
        disabled={disabled || list.isLoading}
        restartRequired={restartRequired}
        description="Connected in AI Services. ChatGPT plans do not include embedding models."
        onChange={(next) => {
          if (next === ADD_PROVIDER) setAdding(true);
          else onChange(next === NO_EMBEDDING_PROVIDER ? "" : next);
        }}
        options={[
          { value: NO_EMBEDDING_PROVIDER, label: "None" },
          ...providerOptions(providers, "embeddings", value),
          ...unable.filter((option) => option.value !== value),
          ...(types.length > 0 ? [{ value: ADD_PROVIDER, label: "Connect a provider..." }] : []),
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
