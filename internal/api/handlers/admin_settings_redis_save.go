package handlers

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/config"
)

// errRedisSettingsMoved refuses a save whose Redis connection another save
// changed while this one was checking it.
var errRedisSettingsMoved = &APIError{
	Status:  http.StatusBadRequest,
	Code:    errCodeInvalidSettings,
	Message: "The Redis settings changed while Silo was checking them. Reload the settings and save again.",
}

// prospectiveSettings returns the stored settings once changes are written
// over them, and the rows the write stores. The rows are changes, except that
// redis.db holds what redisDBRow decides against the URL the write leaves in
// place.
func (h *AdminHandler) prospectiveSettings(stored, changes map[string]string) (prospective, rows map[string]string) {
	prospective = maps.Clone(stored)
	maps.Copy(prospective, changes)
	rows = changes
	if value, ok := changes[config.RedisDBSettingKey]; ok {
		// The same batch can replace redis.url, so the row is decided
		// against the URL the batch leaves in place.
		rows = maps.Clone(changes)
		rows[config.RedisDBSettingKey] = redisDBRow(h.activeAdminSettings(prospective), value)
		prospective[config.RedisDBSettingKey] = rows[config.RedisDBSettingKey]
	}
	return prospective, rows
}

// savedRedisConnection returns the Redis connection a start reads from the
// stored settings values, as config.LoadFromDB does. It reports false when a
// start takes none from them: values name no URL, or REDIS_URL supplies the
// whole connection to this process.
func (h *AdminHandler) savedRedisConnection(values map[string]string) (config.RedisConfig, bool) {
	if h.BootstrapSensitiveConfigured["redis.url"] {
		return config.RedisConfig{}, false
	}
	cfg := config.RedisConfig{URL: values["redis.url"], DB: values[config.RedisDBSettingKey]}
	return cfg, cfg.URL != ""
}

// changedRedisConnection returns the Redis connection the next start takes
// from prospective, and reports whether it differs from the one stored
// settings give it now.
func (h *AdminHandler) changedRedisConnection(stored, prospective map[string]string) (config.RedisConfig, bool) {
	next, ok := h.savedRedisConnection(prospective)
	if !ok {
		return config.RedisConfig{}, false
	}
	current, _ := h.savedRedisConnection(stored)
	return next, next != current
}

// checkRedisSave connects to the Redis a save of changes moves the next start
// to. A start that cannot connect to it stops the server, and the value can
// then no longer be corrected in the admin UI, so the save is refused instead.
// The check runs before the save takes the settings lock and returns the
// connection it checked; confirmRedisSave holds the save to it. A save that
// leaves the connection as it is checks nothing, so a Redis that is down for a
// moment does not block unrelated saves.
func (h *AdminHandler) checkRedisSave(ctx context.Context, changes map[string]string) (config.RedisConfig, error) {
	_, urlChanged := changes["redis.url"]
	_, dbChanged := changes[config.RedisDBSettingKey]
	if !urlChanged && !dbChanged {
		return config.RedisConfig{}, nil
	}
	stored, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return config.RedisConfig{}, err
	}
	prospective, _ := h.prospectiveSettings(stored, changes)
	next, changed := h.changedRedisConnection(stored, prospective)
	if !changed {
		return config.RedisConfig{}, nil
	}
	if err := pingRedis(ctx, next); err != nil {
		slog.WarnContext(ctx, "refused a Redis settings save that Silo could not connect with", "error", err)
		return config.RedisConfig{}, &APIError{Status: http.StatusBadRequest, Code: errCodeInvalidSettings, Message: redisSaveRefusal(err)}
	}
	return next, nil
}

// confirmRedisSave refuses a save whose Redis connection is not the one
// checkRedisSave checked, which happens when another save changed the stored
// settings in between.
func (h *AdminHandler) confirmRedisSave(stored, prospective map[string]string, checked config.RedisConfig) error {
	if next, changed := h.changedRedisConnection(stored, prospective); changed && next != checked {
		return errRedisSettingsMoved
	}
	return nil
}

// redisSaveRefusal explains a failed connection without quoting the error,
// which can name hosts and addresses.
func redisSaveRefusal(err error) string {
	if _, refused := errors.AsType[redis.Error](err); refused {
		return "Redis refused the connection, so the server would not start with these settings. " +
			"Check the database number (0 to 15 on a Redis with the default 16 databases) and the user name and password in redis.url."
	}
	return "Silo could not connect to Redis with these settings, so the server would not start with them. " +
		"Check that Redis is running and that redis.url has the right host and port."
}
