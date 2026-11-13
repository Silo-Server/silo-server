-- +goose Up
-- A file renamed to another title keeps its row, and a missing stand-in row at
-- the old path keeps the old title's links (FileRepository.RetitleMovedFile).
-- stand_in_for_file_id names the row a stand-in stands in for, so a scan that
-- finds the same bytes back at the old path gives that row its path again
-- instead of probing the file onto the stand-in.
--
-- It is a plain column, not a foreign key: ids come from a sequence and are
-- never reused, and it is only read by the stand-in's own id. An id whose row
-- was deleted declines the reclaim, because the moved-file lookup no longer
-- returns that row, and the file written onto the stand-in clears it.
ALTER TABLE public.media_files ADD COLUMN stand_in_for_file_id bigint;

-- +goose Down
ALTER TABLE public.media_files DROP COLUMN stand_in_for_file_id;
