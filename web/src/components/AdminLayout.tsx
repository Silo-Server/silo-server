import { useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router";
import AdminSidebar from "@/components/AdminSidebar";
import ServerActivity from "@/components/ServerActivity";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { resolveAdminDocumentTitle } from "@/lib/documentTitle";
import { Menu, X } from "lucide-react";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";

const ADMIN_DESKTOP_MEDIA_QUERY = "(min-width: 64rem)";

export default function AdminLayout() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const location = useLocation();
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
          <ServerActivity className="h-11 w-11" />
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

      {/* Desktop activity indicator */}
      <div className="fixed top-5 right-5 z-40 hidden lg:block">
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
          <Outlet />
        </div>
      </main>
    </div>
  );
}
