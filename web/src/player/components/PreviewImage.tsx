import { useState } from "react";

interface PreviewImageProps {
  src?: string;
  alt: string;
  className: string;
  placeholderClassName: string;
  iconClassName?: string;
}

/** Keep failed preview delivery independent of navigation and playback. */
export function PreviewImage(props: PreviewImageProps) {
  // Remount the error state for a renewed URL; a late error for the old URL
  // cannot suppress its replacement or a neighboring preview.
  return <PreviewImageSource key={props.src ?? "absent"} {...props} />;
}

function PreviewImageSource({
  src,
  alt,
  className,
  placeholderClassName,
  iconClassName = "h-5 w-5 text-white/[0.12]",
}: PreviewImageProps) {
  const [failed, setFailed] = useState(false);
  if (src && !failed) {
    return <img src={src} alt={alt} className={className} onError={() => setFailed(true)} />;
  }
  return (
    <div aria-hidden="true" className={`${className} ${placeholderClassName}`}>
      <svg
        className={iconClassName}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        <rect x="2" y="2" width="20" height="20" rx="2.18" ry="2.18" />
        <path d="m7 2 0 20M17 2v20M2 12h20M2 7h5M2 17h5M17 17h5M17 7h5" />
      </svg>
    </div>
  );
}
