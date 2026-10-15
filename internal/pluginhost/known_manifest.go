package pluginhost

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// knownManifest returns a copy of the manifest a running plugin reported,
// keeping only what this server's SDK knows: unknown fields are dropped, and
// so are enum values it has no name for. The installed manifest was read from
// JSON with protojson's DiscardUnknown, which drops both, while the plugin's
// own copy arrives over gRPC with them kept. Reading the live copy the same way
// lets a plugin built on a newer SDK match its installed manifest.
func knownManifest(manifest *pluginv1.PluginManifest) *pluginv1.PluginManifest {
	out := proto.Clone(manifest).(*pluginv1.PluginManifest)
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
