import { useEffect, useState } from "react";
import { ArrowUp } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

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
      className={`fixed end-6 bottom-6 z-50 rounded-md transition-opacity duration-150 ${visible ? "pointer-events-auto opacity-100" : "pointer-events-none opacity-0"}`}
      onClick={() => window.scrollTo({ top: 0, behavior: "smooth" })}
    >
      <ArrowUp className="h-4 w-4" />
    </Button>
  );
}
