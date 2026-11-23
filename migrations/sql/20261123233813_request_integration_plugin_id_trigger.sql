-- +goose Up
-- A request server's plugin_id follows the installation it names, whichever
-- release writes the row. During a rolling upgrade a replica still on the
-- previous release saves servers without plugin_id: one it adds would never
-- follow its plugin's reinstall, and one it moves to another plugin would
-- keep the plugin it left, which the rebind would then hand the server and
-- its API key.
--
-- An insert records its installation's plugin, keeping the given plugin_id
-- only when that installation is gone (a restored row). A change of
-- installation records the new one's plugin, or none when it is gone. Any
-- other update keeps plugin_id, so a server whose installation was removed
-- still remembers its plugin until the rebind moves it.
UPDATE request_integrations i
SET plugin_id = p.plugin_id
FROM plugin_installations p
WHERE p.id = i.installation_id AND i.plugin_id <> p.plugin_id;
-- +goose StatementBegin
CREATE FUNCTION set_request_integration_plugin_id() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.plugin_id := coalesce((SELECT plugin_id FROM plugin_installations WHERE id = NEW.installation_id), NEW.plugin_id);
    ELSIF NEW.installation_id IS DISTINCT FROM OLD.installation_id THEN
        NEW.plugin_id := coalesce((SELECT plugin_id FROM plugin_installations WHERE id = NEW.installation_id), '');
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER request_integrations_plugin_id BEFORE INSERT OR UPDATE OF installation_id ON request_integrations FOR EACH ROW EXECUTE FUNCTION set_request_integration_plugin_id();

-- +goose Down
DROP TRIGGER request_integrations_plugin_id ON request_integrations;
DROP FUNCTION set_request_integration_plugin_id();
