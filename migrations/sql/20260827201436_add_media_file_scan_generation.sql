-- +goose Up
CREATE TABLE media_file_scan_generations (
    media_file_id integer PRIMARY KEY REFERENCES media_files(id) ON DELETE CASCADE,
    scan_generation bigint NOT NULL DEFAULT 1
);

-- +goose StatementBegin
CREATE FUNCTION maintain_media_file_scan_generation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO media_file_scan_generations (media_file_id)
        VALUES (NEW.id)
        ON CONFLICT (media_file_id) DO NOTHING;
    ELSE
        INSERT INTO media_file_scan_generations (media_file_id, scan_generation)
        VALUES (NEW.id, 2)
        ON CONFLICT (media_file_id) DO UPDATE
        SET scan_generation = media_file_scan_generations.scan_generation + 1;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER media_files_scan_generation
AFTER INSERT OR UPDATE ON media_files
FOR EACH ROW
EXECUTE FUNCTION maintain_media_file_scan_generation();

INSERT INTO media_file_scan_generations (media_file_id)
SELECT id FROM media_files
ON CONFLICT (media_file_id) DO NOTHING;

-- +goose Down
DROP TRIGGER media_files_scan_generation ON media_files;
DROP FUNCTION maintain_media_file_scan_generation();
DROP TABLE media_file_scan_generations;
