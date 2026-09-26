-- +goose Up
ALTER TABLE playback_v3_attempts
    ADD COLUMN control_recipe_locator JSONB,
    ADD CONSTRAINT playback_attempt_recipe_locator_binding CHECK (
        control_recipe_locator IS NULL OR (
            control_route IS NOT NULL
            AND jsonb_typeof(control_recipe_locator) = 'object'
            AND control_recipe_locator ? 'executor'
            AND control_route ? 'executor'
            AND control_recipe_locator->'executor' = control_route->'executor'
        )
    );

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM playback_v3_attempts WHERE control_recipe_locator IS NOT NULL) THEN
        RAISE EXCEPTION 'Cannot remove published executor recipe locators';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE playback_v3_attempts
    DROP CONSTRAINT playback_attempt_recipe_locator_binding,
    DROP COLUMN control_recipe_locator;
