import { useEffect, useState } from "react";
import { ArrowUp } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { motionScrollBehavior } from "@/hooks/usePrefersReducedMotion";

export default function ScrollToTopButton() {
  const t = useT();
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const onScroll = () => setVisible(window.scrollY > 600);
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  return (
    <Button
      variant="outline"
      size="icon"
      aria-label={t("web.shell.scrollToTop.label")}
      // `invisible` takes the hidden button out of the tab order and the
      // accessibility tree; transitioning `visibility` keeps the fade-out.
      className={`fixed end-6 bottom-6 z-50 rounded-md transition-[opacity,visibility] duration-150 ${visible ? "visible opacity-100" : "invisible opacity-0"}`}
      onClick={() => {
        window.scrollTo({ top: 0, behavior: motionScrollBehavior() });
        // The button hides once the page is back at the top, so hand focus
        // to the page content instead of leaving it on a hidden control.
        document.getElementById("main-content")?.focus({ preventScroll: true });
      }}
    >
      <ArrowUp className="h-4 w-4" />
    </Button>
  );
}
