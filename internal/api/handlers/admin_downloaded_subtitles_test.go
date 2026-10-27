package handlers

import (
	"context"
)

type trackingHandlerBlobStore struct {
	objects     map[string][]byte
	putKeys     []string
	deletedKeys []string
}

func (c *trackingHandlerBlobStore) Put(_ context.Context, key string, data []byte) error {
	if c.objects == nil {
		c.objects = make(map[string][]byte)
	}
	c.objects[key] = append([]byte(nil), data...)
	c.putKeys = append(c.putKeys, key)
	return nil
}

func (c *trackingHandlerBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	if data, ok := c.objects[key]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, context.Canceled
}

func (c *trackingHandlerBlobStore) Delete(_ context.Context, key string) error {
	delete(c.objects, key)
	c.deletedKeys = append(c.deletedKeys, key)
	return nil
}
