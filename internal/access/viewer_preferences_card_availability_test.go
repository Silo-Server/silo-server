package access

import (
	"context"
)

type serverDefaults map[string]string

func (d serverDefaults) Get(_ context.Context, key string) (string, error) { return d[key], nil }
