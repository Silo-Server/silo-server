import { useState } from "react";
import { Outlet, useLocation } from "react-router";
import AdminSidebar from "@/components/AdminSidebar";
import ServerActivity from "@/components/ServerActivity";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { resolveAdminDocumentTitle } from "@/lib/documentTitle";
import { Menu } from "lucide-react";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { useAudiobookPlaybackController } from "@/pages/audiobooks/player/audiobookPlaybackContext";

export default function AdminLayout() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const location = useLocation();
  const { isBackgroundBarVisible } = useWatchPlaybackController();
  const audiobookPlayback = useAudiobookPlaybackController();
  const hasBackgroundBar = isBackgroundBarVisible || audiobookPlayback?.isBackgroundBarVisible;

  useDocumentTitle(resolveAdminDocumentTitle(location.pathname));

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

      {/* Mobile header */}
      <div className="glass-dark border-border/70 sticky top-0 z-30 mx-3 mt-3 flex items-center justify-between rounded-2xl border px-4 py-3 lg:hidden">
        <div className="flex items-center gap-3">
          <button
            onClick={() => setMobileOpen(true)}
            className="text-muted-foreground hover:text-foreground hover:bg-accent/60 flex h-10 w-10 items-center justify-center rounded-xl transition-all"
            aria-label="Open admin menu"
          >
            <Menu className="h-5 w-5" />
          </button>
          <div className="flex items-center gap-2">
            <div className="text-primary border-border/70 bg-surface flex h-9 w-9 items-center justify-center rounded-xl border text-sm font-bold">
              ▶
            </div>
            <span className="text-[15px] font-extrabold tracking-tight">Admin</span>
          </div>
        </div>
        <ServerActivity />
      </div>

      {/* Desktop activity indicator */}
      <div className="fixed top-5 right-5 z-40 hidden lg:block">
        <ServerActivity />
      </div>

      {/* Mobile sidebar drawer */}
      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        <SheetContent side="left" className="w-[260px] p-0 sm:max-w-[260px]">
          <SheetHeader className="sr-only">
            <SheetTitle>Admin Navigation</SheetTitle>
          </SheetHeader>
          <AdminSidebar onNavigate={() => setMobileOpen(false)} />
        </SheetContent>
      </Sheet>

      <main
        id="main-content"
        className={`relative z-10 min-h-screen px-4 py-4 sm:px-6 lg:ml-[240px] lg:px-8 lg:py-8 xl:px-10 ${
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
