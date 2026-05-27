import { ChevronLeft } from "lucide-react";
import { useNavigate } from "react-router";

interface PageBackProps {
  label?: string;
  /**
   * When true, pins the button to the viewport on lg+ so it stays visible
   * while scrolling. The offset matches the app sidebar (260px) so the
   * button sits just inside the page content area.
   */
  floating?: boolean;
}

export default function PageBack({ label = "Go back", floating = false }: PageBackProps) {
  const navigate = useNavigate();
  const position = floating
    ? "absolute top-4 left-2 sm:top-6 lg:fixed lg:left-[268px]"
    : "absolute top-4 left-2 sm:top-6";
  return (
    <button
      type="button"
      aria-label={label}
      onClick={() => navigate(-1)}
      className={`glass text-foreground hover:bg-accent ${position} z-20 flex items-center justify-center rounded-full p-1.5 shadow-md transition-colors`}
    >
      <ChevronLeft className="size-5" />
    </button>
  );
}
