export interface AudiobookSummary {
  content_id: string;
  title: string;
  year: number;
  poster_url?: string;
}

export interface AudiobookListResponse {
  items: AudiobookSummary[];
  total: number;
  limit: number;
  offset: number;
}

export interface AudiobookChapter {
  index: number;
  title: string;
  source: string;
  start_seconds: number;
  end_seconds: number;
}

export interface AudiobookFile {
  id: number;
  path: string;
  duration_seconds: number;
  chapters?: AudiobookChapter[];
}

export interface AudiobookProgress {
  position_seconds: number;
  updated_at: string;
  completed?: boolean;
}

export interface AudiobookDetailItem {
  content_id: string;
  title: string;
  year: number;
  overview?: string;
  poster_url?: string;
}

export interface AudiobookDetailResponse {
  audiobook: AudiobookDetailItem;
  author?: string;
  narrator?: string;
  files: AudiobookFile[];
  progress?: AudiobookProgress;
}
