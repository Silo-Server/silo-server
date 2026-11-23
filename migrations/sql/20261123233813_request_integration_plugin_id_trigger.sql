-- +goose Up
-- A request server's plugin_id follows the installation it names, whichever
-- release writes the row. During a rolling upgrade a replica still on the
-- previous release saves servers without plugin_id: one it adds would never
-- follow its plugin's reinstall, and one it moves to another plugin would
-- keep the plugin it left, which the rebind would then hand the server and
-- its API key.
--
-- An insert, and any change of installation, records the plugin of the
-- installation the row names, or none when it is gone. Any other update keeps
-- plugin_id, so a server whose installation was removed still remembers its
-- plugin until the rebind moves it.
-- +goose StatementBegin
CREATE FUNCTION set_request_integration_plugin_id() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' OR NEW.installation_id IS DISTINCT FROM OLD.installation_id THEN
        NEW.plugin_id := coalesce((SELECT plugin_id FROM plugin_installations WHERE id = NEW.installation_id), '');
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER request_integrations_plugin_id BEFORE INSERT OR UPDATE OF installation_id ON request_integrations FOR EACH ROW EXECUTE FUNCTION set_request_integration_plugin_id();
-- Repair rows written before the trigger, including by a previous-release
-- replica since the column was added. CREATE TRIGGER holds off writers until
-- this migration commits, so no write lands between the two.
UPDATE request_integrations i
SET plugin_id = p.plugin_id
FROM plugin_installations p
WHERE p.id = i.installation_id AND i.plugin_id <> p.plugin_id;

-- +goose Down
DROP TRIGGER request_integrations_plugin_id ON request_integrations;
DROP FUNCTION set_request_integration_plugin_id();
