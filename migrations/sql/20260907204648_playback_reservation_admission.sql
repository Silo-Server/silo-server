-- +goose Up
ALTER TABLE playback_v3_attempts ADD COLUMN control_reservation_admission_id UUID;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM playback_v3_attempts WHERE control_reservation_admission_id IS NOT NULL) THEN
  RAISE EXCEPTION 'cannot remove retained playback reservation admission identity';
 END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts DROP COLUMN control_reservation_admission_id;
