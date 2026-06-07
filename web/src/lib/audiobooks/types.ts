import type {
  AudiobookNarration as ApiAudiobookNarration,
  AudiobookRelatedItem as ApiAudiobookRelatedItem,
  VersionChapter,
} from "@/api/types";

export type AudiobookChapter = VersionChapter;

export interface AudiobookFile {
  id: number;
  path?: string;
  duration_seconds: number;
  chapters?: AudiobookChapter[];
}

export type AudiobookRelatedItem = ApiAudiobookRelatedItem;
export type AudiobookNarration = ApiAudiobookNarration;
