import { useState, useMemo, useCallback, useEffect, useRef, type ChangeEvent } from "react";
import { Check, Loader2, AlertCircle, Upload } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import type { ItemDetail, RemoteImage } from "@/api/types";
import { useItemImages, useApplyItemImage, useUploadItemImage } from "@/hooks/queries/items";
import { cn } from "@/lib/utils";

type ImageTab = "poster" | "backdrop" | "logo";

const IMAGE_TABS: { key: ImageTab; label: string; singular: string; aspect: string }[] = [
  { key: "poster", label: "Posters", singular: "poster", aspect: "aspect-[2/3]" },
  { key: "backdrop", label: "Backdrops", singular: "backdrop", aspect: "aspect-video" },
  { key: "logo", label: "Logos", singular: "logo", aspect: "aspect-video" },
];

// Seasons store a poster and a backdrop; the server rejects logos for them.
const SEASON_TABS = IMAGE_TABS.filter((tab) => tab.key !== "logo");

// The server takes JPEG, PNG and WebP uploads up to 10 MiB.
const UPLOAD_TYPES = ["image/jpeg", "image/png", "image/webp"];
const MAX_UPLOAD_BYTES = 10 * 1024 * 1024;

// An uploaded image is listed only while the item shows it, so it is always
// the current one.
function isUploadedImage(img: RemoteImage) {
  return img.original_url.startsWith("upload://");
}

interface ImageSelectorTabProps {
  item: ItemDetail;
  enabled: boolean;
  onImageApplied?: () => void;
  onApplyPendingChange?: (pending: boolean) => void;
}

export default function ImageSelectorTab({
  item,
  enabled,
  onImageApplied,
  onApplyPendingChange,
}: ImageSelectorTabProps) {
  const availableTabs = item.type === "season" ? SEASON_TABS : IMAGE_TABS;
  const [activeTab, setActiveTab] = useState<ImageTab>("poster");
  const [textlessOnly, setTextlessOnly] = useState(false);
  // null shows every provider's choices.
  const [providerFilter, setProviderFilter] = useState<string | null>(null);
  const [selectedImage, setSelectedImage] = useState<RemoteImage | null>(null);
  // Track which images were applied in this session (original_url per type).
  const [appliedImages, setAppliedImages] = useState<Record<string, string>>({});

  const { data, isLoading, isError } = useItemImages(item.content_id, enabled);
  const applyMutation = useApplyItemImage();
  const uploadMutation = useUploadItemImage();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const pending = applyMutation.isPending || uploadMutation.isPending;

  const images = useMemo(() => data?.images ?? [], [data]);

  useEffect(() => {
    onApplyPendingChange?.(pending);
    return () => onApplyPendingChange?.(false);
  }, [pending, onApplyPendingChange]);

  const current = data?.current;
  const providerErrors = data?.provider_errors;

  const tabConfig = IMAGE_TABS.find((t) => t.key === activeTab)!;

  const { providers, visibleImages, filteredImages, activeProvider } = useMemo(() => {
    const ofType = images.filter((img) => img.type === activeTab);
    const visible =
      textlessOnly && activeTab !== "logo" ? ofType.filter((img) => img.language === "") : ofType;
    // Chips follow the server's order, so local artwork's chip comes first.
    const counts = new Map<string, number>(ofType.map((img) => [img.provider_id, 0]));
    for (const img of visible) counts.set(img.provider_id, (counts.get(img.provider_id) ?? 0) + 1);
    // A filter for a provider that is no longer listed falls back to All.
    const active = providerFilter !== null && counts.has(providerFilter) ? providerFilter : null;
    return {
      providers: [...counts].map(([id, count]) => ({ id, count })),
      visibleImages: visible,
      filteredImages:
        active === null ? visible : visible.filter((img) => img.provider_id === active),
      activeProvider: active,
    };
  }, [images, activeTab, textlessOnly, providerFilter]);

  const selectProvider = (id: string | null) => {
    setProviderFilter(id);
    setSelectedImage(null);
  };

  // The "current" image for this tab. We first check session-local applied
  // images (which use original_url), then fall back to the server's stored
  // path (which may be an S3 key or a plugin path depending on cache state).
  const currentOriginalUrl = appliedImages[activeTab] ?? "";
  const currentStoredPath = useMemo(() => {
    if (!current) return "";
    switch (activeTab) {
      case "poster":
        return current.poster_url ?? "";
      case "backdrop":
        return current.backdrop_url ?? "";
      case "logo":
        return current.logo_url ?? "";
    }
  }, [current, activeTab]);

  const handleApply = useCallback(() => {
    if (!selectedImage) return;
    applyMutation.mutate(
      {
        item,
        request: {
          original_url: selectedImage.original_url,
          type: selectedImage.type,
          provider_id: selectedImage.provider_id,
        },
      },
      {
        onSuccess: () => {
          onImageApplied?.();
          setAppliedImages((prev) => ({
            ...prev,
            [selectedImage.type]: selectedImage.original_url,
          }));
          setSelectedImage(null);
        },
      },
    );
  }, [selectedImage, applyMutation, item, onImageApplied]);

  const handleUpload = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    // Clear the input so choosing the same file again uploads it again.
    event.target.value = "";
    if (!file) return;
    // A browser that cannot name the type leaves it empty; the server checks
    // those bytes itself.
    if (file.type && !UPLOAD_TYPES.includes(file.type)) {
      toast.error("Choose a JPEG, PNG or WebP image");
      return;
    }
    if (file.size > MAX_UPLOAD_BYTES) {
      toast.error("Images can be at most 10 MiB");
      return;
    }
    const type = activeTab;
    uploadMutation.mutate(
      { item, type, file },
      {
        onSuccess: () => {
          onImageApplied?.();
          // The list marks the upload as current once it reloads.
          setAppliedImages((prev) => {
            const next = { ...prev };
            delete next[type];
            return next;
          });
          setSelectedImage(null);
        },
      },
    );
  };

  return (
    <div className="flex h-full flex-col gap-3">
      {/* Info banner */}
      <div className="bg-muted/50 text-muted-foreground shrink-0 rounded-lg px-3 py-2 text-[11px]">
        Image changes apply immediately and are not affected by Cancel.
      </div>

      {item.type === "season" && (
        <div className="flex shrink-0 items-center gap-2 rounded-lg border border-amber-500/20 bg-amber-500/5 px-3 py-1.5 text-[11px] text-amber-600 dark:text-amber-400">
          <AlertCircle className="size-3.5 shrink-0" />
          <span>
            Only seeing one poster? Check for plugin updates and update TMDB and TVDB to load full
            season artwork galleries.
          </span>
        </div>
      )}

      {/* Image type tabs; the filter and upload controls wrap below them on narrow screens */}
      <div className="flex shrink-0 flex-wrap items-center gap-1">
        {availableTabs.map((tab) => (
          <button
            key={tab.key}
            type="button"
            onClick={() => {
              setActiveTab(tab.key);
              setSelectedImage(null);
              setTextlessOnly(false);
              setProviderFilter(null);
            }}
            className={cn(
              "rounded-md px-3 py-1.5 text-[12px] font-medium transition-colors",
              activeTab === tab.key
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:bg-muted hover:text-foreground",
            )}
          >
            {tab.label}
          </button>
        ))}

        <div className="ml-auto flex items-center gap-1">
          {/* Textless filter, hidden for logos */}
          {activeTab !== "logo" && (
            <button
              type="button"
              onClick={() => {
                setTextlessOnly(!textlessOnly);
                // Don't leave a selection the filter hides ready to apply.
                setSelectedImage(null);
              }}
              className={cn(
                "rounded-md px-2.5 py-1.5 text-[11px] font-medium transition-colors",
                textlessOnly
                  ? "bg-primary/10 text-primary"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              Textless
            </button>
          )}

          {/* Upload your own image; it is listed with the local choices. */}
          <button
            type="button"
            onClick={() => fileInputRef.current?.click()}
            disabled={pending}
            aria-label={`Upload ${tabConfig.singular}`}
            className="text-muted-foreground hover:bg-muted hover:text-foreground flex items-center gap-1 rounded-md px-2.5 py-1.5 text-[11px] font-medium transition-colors disabled:pointer-events-none disabled:opacity-50"
          >
            {uploadMutation.isPending ? (
              <Loader2 className="size-3.5 animate-spin" />
            ) : (
              <Upload className="size-3.5" />
            )}
            {uploadMutation.isPending ? "Uploading..." : "Upload"}
          </button>
          <input
            ref={fileInputRef}
            type="file"
            accept={UPLOAD_TYPES.join(",")}
            className="hidden"
            tabIndex={-1}
            aria-hidden="true"
            data-testid="image-upload-input"
            onChange={handleUpload}
          />
        </div>
      </div>

      {/* Provider filter — only useful with more than one source */}
      {providers.length > 1 && (
        <div
          role="group"
          aria-label="Filter by provider"
          className="flex shrink-0 flex-wrap items-center gap-1"
        >
          {[{ id: null, count: visibleImages.length }, ...providers].map((p) => (
            <button
              key={p.id === null ? "all" : `provider:${p.id}`}
              type="button"
              aria-pressed={activeProvider === p.id}
              onClick={() => selectProvider(p.id)}
              className={cn(
                "flex items-center gap-1 rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors",
                activeProvider === p.id
                  ? "bg-primary/10 text-primary"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              {p.id === null ? "All" : p.id.toUpperCase()}
              <span className="tabular-nums opacity-60">{p.count}</span>
            </button>
          ))}
        </div>
      )}

      {/* Provider errors */}
      {providerErrors && Object.keys(providerErrors).length > 0 && (
        <div className="flex shrink-0 items-center gap-2 rounded-lg border border-amber-500/20 bg-amber-500/5 px-3 py-1.5 text-[11px] text-amber-600 dark:text-amber-400">
          <AlertCircle className="size-3.5 shrink-0" />
          <span>
            Could not load from{" "}
            {Object.keys(providerErrors)
              .map((k) => k.toUpperCase())
              .join(", ")}
          </span>
        </div>
      )}

      {/* Image grid */}
      {isLoading ? (
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="text-muted-foreground size-6 animate-spin" />
        </div>
      ) : isError ? (
        <div className="flex flex-1 items-center justify-center">
          <p className="text-muted-foreground text-sm">Failed to load images.</p>
        </div>
      ) : filteredImages.length === 0 ? (
        <div className="flex flex-1 items-center justify-center">
          <p className="text-muted-foreground text-sm">
            {textlessOnly ? "No textless images available." : "No images available."}
          </p>
        </div>
      ) : (
        <ScrollArea className="min-h-0 flex-1">
          <div
            className={cn(
              "grid gap-2 pr-3 pb-1",
              activeTab === "poster" ? "grid-cols-3 sm:grid-cols-4" : "grid-cols-2 sm:grid-cols-3",
            )}
          >
            {filteredImages.map((img, index) => {
              const isSelected = selectedImage === img;
              const isCurrent =
                isUploadedImage(img) ||
                img.original_url === currentOriginalUrl ||
                img.original_url === currentStoredPath;

              return (
                <button
                  key={`${img.provider_id}-${img.original_url}-${index}`}
                  type="button"
                  onClick={() => setSelectedImage(isSelected ? null : img)}
                  className={cn(
                    "group relative overflow-hidden rounded-lg border transition-all",
                    isSelected
                      ? "border-primary ring-primary/30 ring-2"
                      : isCurrent
                        ? "border-primary/50"
                        : "border-border hover:border-foreground/20",
                  )}
                >
                  <div className={cn(tabConfig.aspect, "bg-muted")}>
                    <img
                      src={img.url}
                      alt=""
                      loading="lazy"
                      className="absolute inset-0 h-full w-full object-cover"
                      onError={(e) => {
                        e.currentTarget.style.display = "none";
                      }}
                    />
                  </div>

                  {/* Current indicator */}
                  {isCurrent && (
                    <div className="bg-primary/90 text-primary-foreground absolute top-1 left-1 flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[9px] font-semibold">
                      <Check className="size-2.5" />
                      Current
                    </div>
                  )}

                  {/* Selected indicator */}
                  {isSelected && !isCurrent && (
                    <div className="bg-primary text-primary-foreground absolute top-1 right-1 flex size-5 items-center justify-center rounded-full">
                      <Check className="size-3" />
                    </div>
                  )}

                  {/* Provider badge */}
                  <div className="absolute right-1 bottom-1">
                    <Badge
                      variant="secondary"
                      className="bg-black/60 px-1 py-0 text-[9px] text-white backdrop-blur-sm"
                    >
                      {img.provider_id.toUpperCase()}
                    </Badge>
                  </div>
                </button>
              );
            })}
          </div>
        </ScrollArea>
      )}

      {/* Apply button */}
      {selectedImage && (
        <div className="border-border/50 shrink-0 border-t pt-3">
          <Button onClick={handleApply} disabled={pending} className="w-full" size="sm">
            {applyMutation.isPending ? (
              <>
                <Loader2 className="size-4 animate-spin" />
                Applying...
              </>
            ) : (
              `Apply ${activeTab.charAt(0).toUpperCase() + activeTab.slice(1)}`
            )}
          </Button>
        </div>
      )}
    </div>
  );
}
