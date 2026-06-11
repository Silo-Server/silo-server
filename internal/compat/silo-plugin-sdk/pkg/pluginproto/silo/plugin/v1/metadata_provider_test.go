package pluginv1

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestMetadataItemDescriptor_IncludesReleaseDate(t *testing.T) {
	field := (&MetadataItem{}).ProtoReflect().Descriptor().Fields().ByName("release_date")
	if field == nil {
		t.Fatal("MetadataItem descriptor is missing release_date")
	}
}

func TestGetMetadataRequestDescriptor_IncludesContextFields(t *testing.T) {
	fields := (&GetMetadataRequest{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []string{"provider_ids", "language", "file_path"} {
		if fields.ByName(protoreflect.Name(name)) == nil {
			t.Fatalf("GetMetadataRequest descriptor is missing %s", name)
		}
	}
}

func TestMetadataProviderRequestDescriptors_IncludeProviderContext(t *testing.T) {
	tests := []struct {
		name      string
		fieldName string
		message   protoreflect.ProtoMessage
	}{
		{
			name:      "GetSeasonsRequest",
			fieldName: "provider_ids",
			message:   &GetSeasonsRequest{},
		},
		{
			name:      "GetEpisodesRequest",
			fieldName: "provider_ids",
			message:   &GetEpisodesRequest{},
		},
		{
			name:      "GetImagesRequest provider_ids",
			fieldName: "provider_ids",
			message:   &GetImagesRequest{},
		},
		{
			name:      "GetImagesRequest language",
			fieldName: "language",
			message:   &GetImagesRequest{},
		},
		{
			name:      "SearchMetadataRequest language",
			fieldName: "language",
			message:   &SearchMetadataRequest{},
		},
		{
			name:      "GetSeasonsRequest language",
			fieldName: "language",
			message:   &GetSeasonsRequest{},
		},
		{
			name:      "GetEpisodesRequest language",
			fieldName: "language",
			message:   &GetEpisodesRequest{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.message.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(tt.fieldName)) == nil {
				t.Fatalf("%s descriptor is missing %s", tt.name, tt.fieldName)
			}
		})
	}
}

func TestMetadataProviderServiceDescriptor_IncludesPersonDetailRPC(t *testing.T) {
	method := File_silo_plugin_v1_metadata_provider_proto.Services().
		ByName("MetadataProvider").
		Methods().
		ByName("GetPersonDetail")
	if method == nil {
		t.Fatal("MetadataProvider service descriptor is missing GetPersonDetail")
	}
}

func TestGetPersonDetailRequestDescriptor_IncludesProviderContext(t *testing.T) {
	fields := (&GetPersonDetailRequest{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []string{"provider_ids", "language"} {
		if fields.ByName(protoreflect.Name(name)) == nil {
			t.Fatalf("GetPersonDetailRequest descriptor is missing %s", name)
		}
	}
}

func TestPersonDetailRecordDescriptor_IncludesRefreshFields(t *testing.T) {
	fields := (&PersonDetailRecord{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []string{
		"name",
		"sort_name",
		"bio",
		"birth_date",
		"death_date",
		"birthplace",
		"homepage",
		"photo_path",
		"photo_thumbhash",
		"provider_ids",
	} {
		if fields.ByName(protoreflect.Name(name)) == nil {
			t.Fatalf("PersonDetailRecord descriptor is missing %s", name)
		}
	}
}
