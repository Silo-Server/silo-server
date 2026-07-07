import type { ReactElement, SVGProps } from "react";
import {
  Award,
  Building2,
  Calendar,
  Clock,
  Film,
  Globe,
  Languages,
  LayoutTemplate,
  Monitor,
  Ribbon,
  Shield,
  Star,
  Subtitles,
  Tv,
  Volume2,
  type LucideIcon,
} from "lucide-react";

import type { OverlayIconId } from "./types";

// Lucide icons render directly from the icon ID via this map. Anything not
// here is expected to be a brand mark in BRAND_ICONS below.
const LUCIDE_ICONS: Partial<Record<OverlayIconId, LucideIcon>> = {
  star: Star,
  clock: Clock,
  tv: Tv,
  film: Film,
  award: Award,
  ribbon: Ribbon,
  subtitles: Subtitles,
  languages: Languages,
  building: Building2,
  shield: Shield,
  layout: LayoutTemplate,
  monitor: Monitor,
  volume: Volume2,
  calendar: Calendar,
  globe: Globe,
};

// Inline brand marks. Each is a tiny SVG component that fills currentColor so
// it inherits the badge's text color (the accent color in vibrant mode, white
// otherwise). The visual style is text-as-mark — close enough to evoke the
// brand without using trademarked logo artwork.

function HDRMark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 26 12" fill="currentColor" {...props}>
      <text
        x="13"
        y="9"
        fontSize="10"
        fontWeight="900"
        textAnchor="middle"
        fontFamily="system-ui, sans-serif"
      >
        HDR
      </text>
    </svg>
  );
}

function HDR10Mark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 40 12" fill="currentColor" {...props}>
      <text
        x="20"
        y="9"
        fontSize="10"
        fontWeight="900"
        textAnchor="middle"
        fontFamily="system-ui, sans-serif"
      >
        HDR10
      </text>
    </svg>
  );
}

function DolbyVisionMark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" {...props}>
      <path d="M8 2c3.3 0 6 2.7 6 6s-2.7 6-6 6-6-2.7-6-6 2.7-6 6-6zm0 1.5A4.5 4.5 0 1 0 8 12.5a4.5 4.5 0 0 0 0-9zm0 0v9a4.5 4.5 0 0 0 0-9z" />
    </svg>
  );
}

function AtmosMark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 40 12" fill="currentColor" {...props}>
      <text
        x="20"
        y="9"
        fontSize="9"
        fontWeight="900"
        textAnchor="middle"
        fontFamily="system-ui, sans-serif"
        letterSpacing="0.5"
      >
        ATMOS
      </text>
    </svg>
  );
}

function AV1Mark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 24 12" fill="currentColor" {...props}>
      <text
        x="12"
        y="9"
        fontSize="10"
        fontWeight="900"
        textAnchor="middle"
        fontFamily="system-ui, sans-serif"
      >
        AV1
      </text>
    </svg>
  );
}

function TomatoMark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" {...props}>
      <circle cx="8" cy="9" r="5.5" />
      <path d="M8 3.5c1-1.5 2.5-2 3.5-2C11 3 9.5 3.5 8 3.5z" />
    </svg>
  );
}

type BrandIcon = (props: SVGProps<SVGSVGElement>) => ReactElement;

const BRAND_ICONS: Partial<Record<OverlayIconId, BrandIcon>> = {
  hdr: HDRMark,
  hdr10: HDR10Mark,
  "dolby-vision": DolbyVisionMark,
  atmos: AtmosMark,
  av1: AV1Mark,
  tomato: TomatoMark,
};

interface OverlayIconProps {
  iconId: OverlayIconId;
  size?: number;
  className?: string;
}

export function OverlayIcon({ iconId, size = 10, className }: OverlayIconProps) {
  const Lucide = LUCIDE_ICONS[iconId];
  if (Lucide) {
    return <Lucide size={size} className={className} aria-hidden />;
  }
  const Brand = BRAND_ICONS[iconId];
  if (Brand) {
    // Brand marks use viewBox aspect ratios; width auto-scales from height.
    return <Brand height={size} className={className} aria-hidden />;
  }
  return null;
}
