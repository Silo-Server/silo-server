import { useSearchParams } from "react-router";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useAdminDownloadPreparations } from "@/hooks/queries/admin/downloadPreparations";
import DeviceCopiesTab from "@/pages/admin-downloads/DeviceCopiesTab";
import HistoryTab from "@/pages/admin-downloads/HistoryTab";
import PreparationTab from "@/pages/admin-downloads/PreparationTab";
import PreparedFilesTab from "@/pages/admin-downloads/PreparedFilesTab";
import StorageTab, { type StorageTabTarget } from "@/pages/admin-downloads/StorageTab";
import {
  parseAdminDownloadsTab,
  type AdminDownloadsTab,
} from "@/pages/admin-downloads/adminDownloadsTabs";
import { TabCount } from "@/pages/admin-downloads/controls";
import { activePreparationCount } from "@/pages/adminDownloadPreparationPresentation";

export default function AdminDownloads() {
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseAdminDownloadsTab(searchParams.get("tab"));
  const location = searchParams.get("location") ?? "";
  const activePreparations = activePreparationCount(useAdminDownloadPreparations().data?.counts);

  function open(next: AdminDownloadsTab, extra: Record<string, string> = {}) {
    const params = new URLSearchParams();
    if (next !== "storage") params.set("tab", next);
    for (const [key, value] of Object.entries(extra)) if (value) params.set(key, value);
    setSearchParams(params, { replace: true });
  }

  function navigate(target: StorageTabTarget) {
    if (target === "devices") open("devices", { stale: "1" });
    else open("files", { location: target.files });
  }

  return (
    <div className="space-y-5 lg:space-y-6">
      <div className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3.25rem)]">Downloads</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Preparing downloads, the prepared files on the server and nodes, and the copies on
            people's devices.
          </p>
        </div>
      </div>
      <Tabs
        value={tab}
        onValueChange={(value) => open(parseAdminDownloadsTab(value))}
        className="gap-5 lg:gap-6"
      >
        <TabsList
          variant="line"
          className="border-border w-full justify-start overflow-x-auto overflow-y-hidden border-b"
        >
          <TabsTrigger value="storage" className="flex-none">
            Storage
          </TabsTrigger>
          <TabsTrigger value="preparation" className="flex-none">
            Preparation
            <TabCount count={activePreparations} />
          </TabsTrigger>
          <TabsTrigger value="files" className="flex-none">
            Prepared files
          </TabsTrigger>
          <TabsTrigger value="devices" className="flex-none">
            Device copies
          </TabsTrigger>
          <TabsTrigger value="history" className="flex-none">
            History
          </TabsTrigger>
        </TabsList>
        <TabsContent value="storage">
          <StorageTab onNavigate={navigate} />
        </TabsContent>
        <TabsContent value="preparation">
          <PreparationTab />
        </TabsContent>
        <TabsContent value="files">
          <PreparedFilesTab
            location={location}
            onLocationChange={(next) => open("files", { location: next })}
          />
        </TabsContent>
        <TabsContent value="devices">
          <DeviceCopiesTab
            key={searchParams.get("stale") ?? ""}
            initialStale={searchParams.get("stale") === "1"}
          />
        </TabsContent>
        <TabsContent value="history">
          <HistoryTab />
        </TabsContent>
      </Tabs>
    </div>
  );
}
