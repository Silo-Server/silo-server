package pluginhost

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// KnownManifest returns a copy of a plugin manifest keeping only what this
// server's SDK knows: unknown fields are dropped, and so are enum values it has
// no name for. A manifest from a plugin built on a newer SDK carries both. Read
// from JSON with protojson's DiscardUnknown, unknown fields and unknown enum
// names are dropped but enum numbers are kept; over gRPC, both are kept.
// Comparing two copies of a manifest through KnownManifest ignores whatever a
// newer SDK added, however each copy was read.
func KnownManifest(manifest *pluginv1.PluginManifest) *pluginv1.PluginManifest {
	if manifest == nil {
		return nil
	}
	out := &pluginv1.PluginManifest{}
	proto.Merge(out, manifest)
	dropUnknown(out.ProtoReflect())
	return out
}

func dropUnknown(m protoreflect.Message) {
	m.SetUnknown(nil)
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList():
			dropUnknownInList(fd, v.List())
		case fd.IsMap():
			dropUnknownInMap(fd.MapValue(), v.Map())
		case fd.Enum() != nil:
			if !knownEnum(fd, v.Enum()) {
				m.Clear(fd)
			}
		case fd.Message() != nil:
			dropUnknown(v.Message())
		}
		return true
	})
}

func dropUnknownInList(fd protoreflect.FieldDescriptor, list protoreflect.List) {
	switch {
	case fd.Enum() != nil:
		kept := 0
		for i := range list.Len() {
			if value := list.Get(i); knownEnum(fd, value.Enum()) {
				list.Set(kept, value)
				kept++
			}
		}
		list.Truncate(kept)
	case fd.Message() != nil:
		for i := range list.Len() {
			dropUnknown(list.Get(i).Message())
		}
	}
}

func dropUnknownInMap(value protoreflect.FieldDescriptor, entries protoreflect.Map) {
	var unknown []protoreflect.MapKey
	entries.Range(func(key protoreflect.MapKey, v protoreflect.Value) bool {
		switch {
		case value.Enum() != nil:
			if !knownEnum(value, v.Enum()) {
				unknown = append(unknown, key)
			}
		case value.Message() != nil:
			dropUnknown(v.Message())
		}
		return true
	})
	for _, key := range unknown {
		entries.Clear(key)
	}
}

func knownEnum(fd protoreflect.FieldDescriptor, number protoreflect.EnumNumber) bool {
	return fd.Enum().Values().ByNumber(number) != nil
}
