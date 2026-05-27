import { ChevronLeft } from "lucide-react";
import { useNavigate } from "react-router";

interface PageBackProps {
  label?: string;
}

export default function PageBack({ label = "Go back" }: PageBackProps) {
  const navigate = useNavigate();
  return (
    <button
      type="button"
      aria-label={label}
      onClick={() => navigate(-1)}
      className="glass-subtle text-muted-foreground hover:text-foreground absolute top-4 left-4 z-20 flex items-center justify-center rounded-full p-1.5 transition-colors sm:top-6 sm:left-6"
    >
      <ChevronLeft className="size-5" />
    </button>
  );
}
