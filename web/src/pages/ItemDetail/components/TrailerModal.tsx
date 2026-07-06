import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import type { ItemVideo } from "@/api/types";
import { extraKindLabel } from "@/lib/extraKinds";

interface TrailerModalProps {
  video: ItemVideo | null;
  onOpenChange: (open: boolean) => void;
}

/**
 * Plays a remote provider video (YouTube only for now) in a privacy-enhanced
 * embed. Rendered open whenever `video` is set; closes via the dialog
 * primitive's backdrop/Escape/close-button behavior.
 */
export default function TrailerModal({ video, onOpenChange }: TrailerModalProps) {
  const title = video?.name || (video ? extraKindLabel(video.kind) : "");

  return (
    <Dialog open={video !== null} onOpenChange={onOpenChange}>
      <DialogContent className="gap-0 overflow-hidden border-none bg-black p-0 sm:max-w-4xl">
        <DialogTitle className="sr-only">{title}</DialogTitle>
        <DialogDescription className="sr-only">Trailer video player</DialogDescription>
        {video && (
          <iframe
            src={`https://www.youtube-nocookie.com/embed/${video.site_key}?autoplay=1`}
            allow="autoplay; encrypted-media; fullscreen"
            title={title}
            className="aspect-video w-full"
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
