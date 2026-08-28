import { ChevronLeft } from "lucide-react";
import { type To, useNavigate } from "react-router";

import { hasRouterHistory } from "@/lib/backNavigation";

interface PageBackProps {
  label?: string;
  to?: To;
  preferHistory?: boolean;
  /**
   * When true, pins the button to the viewport on lg+ so it stays visible
   * while scrolling. The offset matches the app sidebar (260px) so the
   * button sits just inside the page content area.
   */
  floating?: boolean;
  /** Use the app's route cross-fade when following an explicit target. */
  viewTransition?: boolean;
}

export default function PageBack({
  label = "Go back",
  to = "/",
  preferHistory = true,
  floating = false,
  viewTransition = false,
}: PageBackProps) {
  const navigate = useNavigate();
  const position = floating
    ? "absolute top-4 left-2 sm:top-6 lg:fixed lg:left-[268px]"
    : "absolute top-4 left-2 sm:top-6";

  function goBack() {
    if (preferHistory && hasRouterHistory()) {
      navigate(-1);
      return;
    }

    if (viewTransition) {
      navigate(to, { viewTransition: true });
      return;
    }

    navigate(to);
  }

  return (
    <button
      type="button"
      aria-label={label}
      onClick={goBack}
      className={`glass glass-hover glass-hover-accent text-foreground ${position} z-20 flex items-center justify-center rounded-full p-1.5 shadow-md`}
    >
      <ChevronLeft className="size-5" />
    </button>
  );
}
