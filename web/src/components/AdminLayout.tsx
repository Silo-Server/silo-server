import { useEffect, useMemo, useState } from "react";
import { Outlet, useLocation } from "react-router";
import AdminSidebar from "@/components/AdminSidebar";
import { AdminSectionCommandDialog } from "@/components/AdminSectionCommandDialog";
import ServerActivity from "@/components/ServerActivity";
import { RestartBanner } from "@/components/admin/RestartBanner";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { usePolicyCapability } from "@/hooks/queries/admin/policy";
import { useAdminServerStatus } from "@/hooks/queries/admin/settings";
import { buildAdminCommandNavSections } from "@/lib/adminNavigation";
import { resolveAdminDocumentTitle } from "@/lib/documentTitle";
import { searchShortcutLabel } from "@/lib/keyboardShortcut";
import { cn } from "@/lib/utils";
import { Menu, Search, X } from "lucide-react";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";

const ADMIN_DESKTOP_MEDIA_QUERY = "(min-width: 64rem)";

export default function AdminLayout() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const [commandOpen, setCommandOpen] = useState(false);
  const location = useLocation();
  const { data: adminInstallations } = useAdminPluginInstallations();
  const policyCapability = usePolicyCapability();
  // The one restart prompt for the admin area. Read here, not in a page: a
  // restart is owed by the server, not by the page that happened to ask for
  // it, so the prompt has to survive navigating away from settings. The query
  // is shared and cached, so the settings overview reading the same status
  // costs nothing extra.
  const { data: serverStatus } = useAdminServerStatus();
  // Mounted here rather than on the dashboard so Cmd+K reaches every admin
  // page, which is what the pages that advertise the shortcut assume.
  const adminSearchSections = useMemo(
    () =>
      buildAdminCommandNavSections(adminInstallations, {
        policyEditorAvailable: policyCapability.data?.editor_available === true,
      }),
    [adminInstallations, policyCapability.data?.editor_available],
  );
  const { isBackgroundBarVisible } = useWatchPlaybackController();
  const audiobookPlayback = useAudiobookPlaybackController();
  const hasBackgroundBar = isBackgroundBarVisible || audiobookPlayback?.isBackgroundBarVisible;
  const documentTitle = resolveAdminDocumentTitle(location.pathname);
  const mobileTitle =
    documentTitle === "Admin" ? "Dashboard" : documentTitle.replace(/^Admin /, "");

  useDocumentTitle(documentTitle);

  useEffect(() => {
    const desktopMedia = window.matchMedia(ADMIN_DESKTOP_MEDIA_QUERY);
    const closeMobileNavigation = (event: MediaQueryListEvent) => {
      if (event.matches) {
        setMobileOpen(false);
      }
    };

    desktopMedia.addEventListener("change", closeMobileNavigation);
    return () => desktopMedia.removeEventListener("change", closeMobileNavigation);
  }, []);

  return (
    <div className="bg-background relative min-h-[100dvh] overflow-x-hidden">
      <AdminSectionCommandDialog
        sections={adminSearchSections}
        open={commandOpen}
        onOpenChange={setCommandOpen}
      />
      <a
        href="#main-content"
        className="focus:bg-background focus:text-foreground focus:ring-ring sr-only focus:not-sr-only focus:fixed focus:top-4 focus:left-4 focus:z-50 focus:rounded-lg focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:ring-2 focus:outline-none"
      >
        Skip to content
      </a>
      <div className="from-primary/6 pointer-events-none fixed inset-x-0 top-0 z-0 h-40 bg-gradient-to-b to-transparent blur-3xl" />
      {/* Desktop sidebar */}
      <div className="hidden lg:block">
        <AdminSidebar />
      </div>

      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        {/* Mobile header */}
        <div className="glass-dark border-border/70 sticky top-0 z-30 mx-3 mt-3 flex items-center justify-between rounded-2xl border px-3 py-2.5 lg:hidden">
          <div className="flex min-w-0 items-center gap-2.5">
            <SheetTrigger asChild>
              <button
                className="text-muted-foreground hover:text-foreground hover:bg-accent/60 focus-visible:ring-ring/60 flex h-11 w-11 shrink-0 items-center justify-center rounded-xl transition-all focus-visible:ring-[3px] focus-visible:outline-none"
                aria-label="Open admin navigation"
              >
                <Menu className="h-5 w-5" />
              </button>
            </SheetTrigger>
            <div className="flex min-w-0 items-center gap-2">
              <div className="text-primary border-border/70 bg-surface flex h-9 w-9 shrink-0 items-center justify-center rounded-xl border text-sm font-bold">
                ▶
              </div>
              <div className="min-w-0">
                <span className="text-muted-foreground block text-[10px] leading-none font-semibold tracking-[0.16em] uppercase">
                  Admin
                </span>
                <span className="mt-1 block truncate text-[15px] leading-none font-extrabold tracking-tight">
                  {mobileTitle}
                </span>
              </div>
            </div>
          </div>
          <div className="flex items-center gap-1">
            <AdminSearchButton onClick={() => setCommandOpen(true)} className="h-11 w-11" />
            <ServerActivity className="h-11 w-11" />
          </div>
        </div>

        {/* Mobile sidebar drawer */}
        <SheetContent
          side="left"
          showCloseButton={false}
          onCloseAutoFocus={(event) => {
            if (window.matchMedia(ADMIN_DESKTOP_MEDIA_QUERY).matches) {
              event.preventDefault();
              document.getElementById("main-content")?.focus({ preventScroll: true });
            }
          }}
          className="w-[320px] max-w-[calc(100vw-3rem)] gap-0 border-r-0 p-0 sm:max-w-[320px]"
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Admin Navigation</SheetTitle>
          </SheetHeader>
          <SheetClose asChild>
            <button
              type="button"
              aria-label="Close admin navigation"
              className="text-muted-foreground hover:text-foreground hover:bg-accent focus-visible:ring-ring/60 absolute top-4 right-4 z-10 flex h-11 w-11 items-center justify-center rounded-xl transition-colors focus-visible:ring-[3px] focus-visible:outline-none"
            >
              <X className="h-5 w-5" aria-hidden="true" />
            </button>
          </SheetClose>
          <AdminSidebar embedded onNavigate={() => setMobileOpen(false)} />
        </SheetContent>
      </Sheet>

      {/* Desktop header controls */}
      <div className="fixed top-5 right-5 z-40 hidden items-center gap-2 lg:flex">
        <AdminSearchButton onClick={() => setCommandOpen(true)} showShortcut />
        <ServerActivity />
      </div>

      <main
        id="main-content"
        tabIndex={-1}
        className={`relative z-10 min-h-screen min-w-0 px-4 py-4 sm:px-6 lg:ml-[240px] lg:px-8 lg:py-8 xl:px-10 ${
          hasBackgroundBar ? "pb-32 sm:pb-36" : ""
        }`}
      >
        <div className="admin-shell">
          {/* Above the routed page and inside the content column, so every
              admin page carries the prompt and none of them can be covered by
              it. `lg:mt-7` clears the fixed Search/activity controls in the
              top-right corner (top-5, h-9 → they end 3.5rem down), which would
              otherwise float over the banner's buttons. */}
          <RestartBanner
            restartRequired={serverStatus?.restart_required}
            restartSignal={serverStatus?.restart_mark_count}
            className="lg:mt-7"
          />
          <Outlet />
        </div>
      </main>
    </div>
  );
}

function AdminSearchButton({
  onClick,
  className,
  showShortcut = false,
}: {
  onClick: () => void;
  className?: string;
  showShortcut?: boolean;
}) {
  // Advertised, not hardcoded: the dialog opens on either modifier, so the hint
  // has to name the one this keyboard actually has.
  const shortcut = searchShortcutLabel();

  return (
    <button
      type="button"
      onClick={onClick}
      aria-label="Search admin sections"
      title={`Search admin sections (${shortcut})`}
      className={cn(
        "text-muted-foreground hover:text-foreground hover:bg-accent/60 focus-visible:ring-ring/60 border-border/70 bg-surface/70 flex h-9 items-center justify-center gap-2 rounded-xl border px-2.5 transition-colors focus-visible:ring-[3px] focus-visible:outline-none",
        className,
      )}
    >
      <Search className="h-4 w-4" aria-hidden="true" />
      {showShortcut ? (
        <>
          <span className="hidden text-[13px] font-medium xl:inline">Search</span>
          <kbd className="border-border/70 pointer-events-none rounded border px-1.5 py-0.5 font-mono text-[10px] whitespace-nowrap select-none">
            {shortcut}
          </kbd>
        </>
      ) : null}
    </button>
  );
}
