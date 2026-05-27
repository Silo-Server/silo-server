import { ChevronLeft } from "lucide-react";
import { type To, useNavigate } from "react-router";

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
}

export default function PageBack({
  label = "Go back",
  to = "/",
  preferHistory = true,
  floating = false,
}: PageBackProps) {
  const navigate = useNavigate();
  const position = floating
    ? "absolute top-4 left-2 sm:top-6 lg:fixed lg:left-[268px]"
    : "absolute top-4 left-2 sm:top-6";

  function goBack() {
    const historyIndex = window.history.state?.idx;

    if (preferHistory && typeof historyIndex === "number" && historyIndex > 0) {
      navigate(-1);
      return;
    }

    navigate(to);
  }

  return (
    <button
      type="button"
      aria-label={label}
      onClick={goBack}
      className={`glass text-foreground hover:bg-accent ${position} z-20 flex items-center justify-center rounded-full p-1.5 shadow-md transition-colors`}
    >
      <ChevronLeft className="size-5" />
    </button>
  );
}
