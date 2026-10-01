import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { Plus, Trash2, Upload, X } from "lucide-react";

import type { PluginRepository } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import {
  useCreatePluginRepository,
  useDeletePluginRepository,
  usePluginUpload,
  useUpdatePluginRepository,
} from "@/hooks/queries/admin/plugins";
import { cn } from "@/lib/utils";

const PANEL = "rounded-xl border bg-card px-4 py-4";

/** "Install from a file": upload a plugin archive that isn't in a repository. */
export function PluginUploadPanel() {
  const { upload, progress, isPending } = usePluginUpload();
  const [file, setFile] = useState<File | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (!file || isPending) return;
    upload(file, {
      onSuccess: () => {
        setFile(null);
        // Without this, choosing the same file again fires no change event.
        if (inputRef.current) inputRef.current.value = "";
      },
    });
  }

  return (
    <section className={PANEL} aria-labelledby="plugin-upload-heading">
      <h2 id="plugin-upload-heading" className="text-[15px] font-semibold">
        Install from a file
      </h2>
      <p className="text-muted-foreground text-[13px]">
        Upload a plugin file for a plugin that isn&apos;t in a repository.
      </p>
      <form onSubmit={handleSubmit} className="mt-3 flex flex-col gap-2.5 sm:flex-row">
        <label className="border-border hover:border-foreground/20 focus-within:ring-ring flex h-9 min-w-0 flex-1 cursor-pointer items-center gap-2 rounded-lg border border-dashed px-3.5 text-sm transition-colors focus-within:ring-2">
          <Upload className="text-muted-foreground h-4 w-4 shrink-0" aria-hidden />
          <span className="text-muted-foreground truncate">
            {file ? file.name : "Choose plugin file..."}
          </span>
          <input
            ref={inputRef}
            type="file"
            aria-label="Plugin file"
            className="sr-only"
            disabled={isPending}
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
        </label>
        <Button type="submit" variant="outline" size="sm" disabled={!file || isPending}>
          <Upload className="h-3.5 w-3.5" />
          {isPending ? "Uploading..." : "Upload"}
        </Button>
      </form>
      {progress !== null && (
        <Progress value={progress} aria-label="Plugin upload progress" className="mt-3" />
      )}
    </section>
  );
}

export function PluginRepositoriesPanel({
  repositories,
  repositoriesError,
}: {
  repositories: PluginRepository[];
  repositoriesError: unknown;
}) {
  const createRepository = useCreatePluginRepository();
  const updateRepository = useUpdatePluginRepository();
  const deleteRepository = useDeletePluginRepository();

  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [showForm, setShowForm] = useState(false);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim() || !url.trim() || createRepository.isPending) return;
    // Keep what the admin typed until the server accepts it, so a rejected URL can be fixed.
    createRepository.mutate(
      { display_name: name.trim(), url: url.trim(), enabled: true },
      {
        onSuccess: () => {
          setName("");
          setUrl("");
          setShowForm(false);
        },
      },
    );
  }

  return (
    <section className={PANEL} aria-labelledby="plugin-repositories-heading">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 id="plugin-repositories-heading" className="text-[15px] font-semibold">
            Repositories
          </h2>
          <p className="text-muted-foreground text-[13px]">Where Silo finds plugins.</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setShowForm(!showForm)}>
          {showForm ? <X className="h-3.5 w-3.5" /> : <Plus className="h-3.5 w-3.5" />}
          {showForm ? "Cancel" : "Add repository"}
        </Button>
      </div>

      {repositoriesError ? (
        <p role="alert" className="text-destructive mt-3 text-sm">
          Failed to load plugin repositories.
        </p>
      ) : null}

      {showForm && (
        <form onSubmit={handleSubmit} className="mt-3 flex flex-col gap-2 sm:flex-row">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Repository name"
            aria-label="Repository name"
            className="sm:flex-1"
          />
          <Input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://plugins.example.test/index.json"
            aria-label="Repository URL"
            className="sm:flex-[2]"
          />
          <Button type="submit" size="sm" disabled={createRepository.isPending}>
            Add
          </Button>
        </form>
      )}

      {repositories.length > 0 ? (
        <ul className="mt-2 divide-y">
          {repositories.map((repo) => (
            <li key={repo.id} className="flex min-w-0 items-center gap-2.5 py-2 text-[13.5px]">
              <span
                aria-hidden="true"
                className={cn(
                  "size-1.5 shrink-0 rounded-full",
                  repo.enabled ? "bg-success" : "bg-muted-foreground",
                )}
              />
              <span className="shrink-0 font-medium">{repo.display_name}</span>
              <span className="text-muted-foreground min-w-0 flex-1 truncate text-[12.5px]">
                {repo.managed ? "Managed by Silo" : repo.url}
              </span>
              {repo.managed ? null : (
                <span className="flex shrink-0 gap-1">
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() =>
                      updateRepository.mutate({ id: repo.id, body: { enabled: !repo.enabled } })
                    }
                  >
                    {repo.enabled ? "Disable" : "Enable"}
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    aria-label={`Remove ${repo.display_name}`}
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() => deleteRepository.mutate(repo.id)}
                  >
                    <Trash2 className="h-3 w-3" />
                  </Button>
                </span>
              )}
            </li>
          ))}
        </ul>
      ) : null}

      {repositories.length === 0 && !showForm && !repositoriesError ? (
        <p className="text-muted-foreground mt-3 text-sm">
          No repositories configured. Add one to browse available plugins.
        </p>
      ) : null}
    </section>
  );
}
