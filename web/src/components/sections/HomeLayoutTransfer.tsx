import { useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { useReturnFocus } from "@/components/homeRows/useReturnFocus";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import { v2 } from "@/api/v2/request";
import {
  layoutPageQuery as pageQuery,
  layoutProblemMessage as problemMessage,
} from "@/hooks/queries/homeRows/useHomeLayoutExport";
import { sectionKeys } from "@/hooks/queries/keys";
import { invalidateSettingValueQueries } from "@/hooks/queries/settingValues";
import { useOptionalAuth } from "@/hooks/useAuth";
import { fetchRecipeCatalog } from "@/lib/recipes";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { randomUUID } from "@/lib/uuid";
import {
  HOME_LAYOUT_MAX_LENGTH,
  HOME_LAYOUT_SKIP_REASON_LABELS,
  importPage,
  parseHomeLayoutFile,
  planHomeLayoutImport,
  fileReferences,
  type HomeLayoutImportPlan,
  type HomeLayoutReferences,
} from "@/lib/homeLayoutTransfer";

const NO_IDS: ReadonlySet<string> = new Set();
const NO_REFERENCES: HomeLayoutReferences = { personalCollections: false, profiles: false };

function listNames(names: string[]): string {
  return names.join(", ");
}

interface HomeLayoutImportDialogProps {
  onClose: () => void;
  libraries: { id: number; name: string; type: string }[];
}

/** Import of a Home layout file into this profile (#1705), opened from More. */
export function HomeLayoutImportDialog({ onClose, libraries }: HomeLayoutImportDialogProps) {
  const qc = useQueryClient();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [text, setText] = useState("");
  const [applying, setApplying] = useState(false);
  // The profile this dialog imports into. Its reads and writes carry this
  // authority, so a profile switch can't redirect an import in progress.
  const [profileContext] = useState(captureProfileRequestContext);
  // Opened from More, which gets focus back on close.
  const returnFocus = useReturnFocus();

  const identityQuery = useQuery({
    queryKey: ["home-layout-import", "server-identity"],
    queryFn: () => v2("GET /api/v2/system/identity"),
    staleTime: Infinity,
  });
  const parsed = useMemo(() => (text.trim() ? parseHomeLayoutFile(text) : null), [text]);
  // Personal collections and profiles are read only for a same-server file
  // that names one; another server's references are skipped without looking
  // them up.
  const sameServerFile =
    parsed?.ok &&
    identityQuery.data &&
    parsed.file.server_id !== "" &&
    parsed.file.server_id === identityQuery.data.server_id
      ? parsed.file
      : null;
  const refs = useMemo(
    () => (sameServerFile ? fileReferences(sameServerFile) : NO_REFERENCES),
    [sameServerFile],
  );
  const scoped = { profileContext: profileContext ?? undefined };
  // These sets belong to the acting profile, so they are read fresh on each
  // open and never reused from cache.
  const collectionsQuery = useQuery({
    queryKey: ["home-layout-import", "personal-collection-ids"],
    queryFn: async () =>
      new Set((await v2("GET /api/v2/collections", scoped)).items.map((item) => item.id)),
    enabled: refs.personalCollections,
    gcTime: 0,
  });
  const profilesQuery = useQuery({
    queryKey: ["home-layout-import", "account-profile-ids"],
    queryFn: async () =>
      new Set((await v2("GET /api/v2/profiles", scoped)).items.map((item) => item.id)),
    enabled: refs.profiles,
    gcTime: 0,
  });
  const personalCollectionIds = refs.personalCollections ? collectionsQuery.data : NO_IDS;
  const profileIds = refs.profiles ? profilesQuery.data : NO_IDS;
  // Shares the Home screen settings page's cache entry.
  const recipeCatalogQuery = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: 5 * 60 * 1000,
  });
  const recipeCatalog = recipeCatalogQuery.data;
  // Only an admin adds new rows of an admin-only kind (Editor's picks).
  const allowAdminOnlyRecipes = useOptionalAuth()?.user?.role === "admin";

  const targetReady = Boolean(
    identityQuery.data && personalCollectionIds && profileIds && recipeCatalog,
  );
  const targetError =
    identityQuery.isError ||
    (refs.personalCollections && collectionsQuery.isError) ||
    (refs.profiles && profilesQuery.isError) ||
    recipeCatalogQuery.isError;

  const plan = useMemo<HomeLayoutImportPlan | null>(() => {
    if (
      !parsed?.ok ||
      !identityQuery.data ||
      !personalCollectionIds ||
      !profileIds ||
      !recipeCatalog
    ) {
      return null;
    }
    const recipes = new Map<string, { adminOnly: boolean }>();
    for (const defs of Object.values(recipeCatalog.categories)) {
      for (const def of defs ?? []) recipes.set(def.type, { adminOnly: def.admin_only });
    }
    return planHomeLayoutImport(
      parsed.file,
      {
        serverId: identityQuery.data.server_id,
        libraries,
        recipes,
        allowAdminOnlyRecipes,
        personalCollectionIds,
        profileIds,
      },
      randomUUID,
    );
  }, [
    parsed,
    identityQuery.data,
    personalCollectionIds,
    profileIds,
    recipeCatalog,
    libraries,
    allowAdminOnlyRecipes,
  ]);

  const hasChanges = Boolean(
    plan && (plan.pages.length > 0 || plan.hideWatchedItems !== undefined),
  );

  async function handleFile(file: File) {
    if (file.size > HOME_LAYOUT_MAX_LENGTH) {
      toast.error("This file is too large to be a home layout export.");
      return;
    }
    try {
      setText(await file.text());
    } catch {
      toast.error("Couldn't read that file");
    }
  }

  async function handleImport() {
    if (!plan || !hasChanges) return;
    // The plan was checked against this profile's libraries and collections.
    if (!profileContext || !isCapturedProfileAuthorityActive(profileContext)) {
      toast.error("The active profile changed. Close this dialog and import again.");
      return;
    }
    setApplying(true);
    const failures: string[] = [];
    const keptSavedChanges: string[] = [];
    for (const page of plan.pages) {
      try {
        const query = pageQuery(page.scope, page.libraryId);
        const { keptSavedChanges: kept } = await importPage(
          page,
          plan.sameServer,
          {
            listSaved: () =>
              v2("GET /api/v2/profile/sections", { query, profileContext }).then(
                (result) => result.items,
              ),
            listView: () =>
              v2("GET /api/v2/profile/sections/settings", { query, profileContext }).then(
                (result) => result.items,
              ),
            save: (overrides) =>
              v2("PUT /api/v2/profile/sections", { query, body: { overrides }, profileContext }),
          },
          randomUUID,
        );
        if (kept) keptSavedChanges.push(page.label);
      } catch (error) {
        failures.push(`${page.label} (${problemMessage(error)})`);
      }
    }
    if (plan.hideWatchedItems !== undefined) {
      try {
        // A direct request, not a mutation: the captured context holds tokens
        // that mustn't sit in the mutation cache.
        await v2("PUT /api/v2/settings/values/{key}", {
          path: { key: SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS },
          query: { scope: "profile" },
          body: { value: plan.hideWatchedItems },
          profileContext,
        });
      } catch (error) {
        failures.push(`Hide watched items (${problemMessage(error)})`);
      }
      // Refresh after a failure too: a write that timed out may still have landed.
      await invalidateSettingValueQueries(
        qc,
        { scope: "profile" },
        SETTING_KEYS.HOME_HIDE_WATCHED_ITEMS,
      );
    }
    await qc.invalidateQueries({ queryKey: sectionKeys.all });
    setApplying(false);
    if (failures.length === 0) {
      toast.success(
        "Home layout imported",
        keptSavedChanges.length > 0
          ? {
              description: `This profile's own changes to some sections were kept on: ${keptSavedChanges.join(", ")}.`,
            }
          : undefined,
      );
      onClose();
    } else {
      toast.error(`Some parts didn't import: ${failures.join("; ")}`);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !applying) onClose();
      }}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl" {...returnFocus}>
        <DialogHeader>
          <DialogTitle>Import home layout</DialogTitle>
          <DialogDescription>
            Load a layout exported from another profile or another Silo server. You'll see what
            changes before anything is saved.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="flex items-center justify-between gap-2">
            <Label htmlFor="home-layout-import-text" className="text-sm font-medium">
              Layout file
            </Label>
            <Button
              size="sm"
              variant="outline"
              onClick={() => fileInputRef.current?.click()}
              disabled={applying}
            >
              Choose file…
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              accept=".json,application/json"
              className="hidden"
              onChange={(event) => {
                const selected = event.target.files?.[0];
                if (selected) void handleFile(selected);
                event.target.value = "";
              }}
            />
          </div>
          <textarea
            id="home-layout-import-text"
            value={text}
            onChange={(event) => setText(event.target.value)}
            placeholder="Or paste the exported JSON here"
            spellCheck={false}
            disabled={applying}
            className="border-input bg-background ring-offset-background placeholder:text-muted-foreground focus-visible:ring-ring min-h-28 w-full rounded-md border px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-offset-2"
          />

          {parsed && !parsed.ok ? <p className="text-destructive text-sm">{parsed.error}</p> : null}
          {parsed?.ok && targetError ? (
            <p className="text-destructive text-sm">
              Couldn't load this server's details. Close the dialog and try again.
            </p>
          ) : null}
          {parsed?.ok && !targetReady && !targetError ? (
            <p className="text-muted-foreground text-sm">
              Checking the layout against this server…
            </p>
          ) : null}
          {plan ? <HomeLayoutImportSummary plan={plan} hasChanges={hasChanges} /> : null}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={applying}>
            Cancel
          </Button>
          <Button onClick={() => void handleImport()} disabled={!hasChanges || applying}>
            {applying ? "Importing…" : "Import"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function HomeLayoutImportSummary({
  plan,
  hasChanges,
}: {
  plan: HomeLayoutImportPlan;
  hasChanges: boolean;
}) {
  const pageNames = plan.pages.map((page) => page.label);
  return (
    <div className="surface-panel-subtle space-y-2 rounded-lg p-3 text-sm">
      {!hasChanges ? <p>Nothing in this file applies to this profile on this server.</p> : null}
      {pageNames.length > 0 && plan.sameServer ? (
        <p>
          Replaces this profile's layout on: <strong>{listNames(pageNames)}</strong>.
        </p>
      ) : null}
      {pageNames.length > 0 && !plan.sameServer ? (
        <p>
          This layout comes from another server. Its own sections are added to:{" "}
          <strong>{listNames(pageNames)}</strong>, replacing sections you added here before. Your
          changes to this server's sections stay.
        </p>
      ) : null}
      {plan.hideWatchedItems !== undefined ? (
        <p>Turns Hide watched items {plan.hideWatchedItems ? "on" : "off"}.</p>
      ) : null}
      {plan.skippedServerSectionChanges > 0 ? (
        <p className="text-muted-foreground">
          {plan.skippedServerSectionChanges === 1
            ? "1 change to a section defined by the other server's admin doesn't apply here."
            : `${plan.skippedServerSectionChanges} changes to sections defined by the other server's admin don't apply here.`}
        </p>
      ) : null}
      {plan.skippedPages.length > 0 ? (
        <p className="text-muted-foreground">
          No library here matches: {listNames(plan.skippedPages)}.
        </p>
      ) : null}
      {plan.skippedSections.length > 0 ? (
        <div className="text-muted-foreground space-y-1">
          <p>These sections are skipped:</p>
          <ul className="list-disc space-y-0.5 pl-5">
            {plan.skippedSections.map((section, index) => (
              <li key={`${section.page}-${section.title}-${index}`}>
                {section.title} ({section.page}): {HOME_LAYOUT_SKIP_REASON_LABELS[section.reason]}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
