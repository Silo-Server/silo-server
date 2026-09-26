package storagetransition

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// Source aliases expose the same physical objects through different prefixes.
// Rejecting writes after the fence checks that probing finishes before fencing.
type sourceAliasStore struct {
	*aliasStore
	fenced bool
}

func (s *sourceAliasStore) view() *memoryStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	objects := make(map[string][]byte)
	for key, value := range s.backing {
		if s.prefix == "" || strings.HasPrefix(key, s.prefix+"/") {
			objects[strings.TrimPrefix(key, s.physical(""))] = value
		}
	}
	return &memoryStore{objects: objects}
}

func (s *sourceAliasStore) Get(ctx context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	return s.view().Get(ctx, key)
}

func (s *sourceAliasStore) List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	return s.view().List(ctx, prefix, cursor, limit)
}

func (s *sourceAliasStore) Put(ctx context.Context, key string, data []byte) error {
	if s.fenced {
		return errors.New("source writes are fenced")
	}
	return s.aliasStore.Put(ctx, key, data)
}

func (s *sourceAliasStore) BeginMutationFence(context.Context) (func(), error) {
	s.fenced = true
	return func() { s.fenced = false }, nil
}

func TestExecuteSeparatesSourceEndpointAliases(t *testing.T) {
	for _, layout := range []string{"private_nested", "public_nested", "shared", "distinct", "probe_error", "cleanup_error"} {
		for _, policy := range []string{PolicyPreserveUploads, PolicyMigrateAll, PolicyFresh} {
			t.Run(layout+"/"+policy, func(t *testing.T) {
				publicPrefix, privatePrefix := "", "branding/private"
				switch layout {
				case "public_nested":
					publicPrefix, privatePrefix = "public", ""
				case "shared":
					privatePrefix = ""
				}
				backing := make(map[string][]byte)
				newSource := func(endpoint, prefix string) *sourceAliasStore {
					return &sourceAliasStore{aliasStore: &aliasStore{
						memoryStore: &memoryStore{identity: "s3|" + endpoint + "|shared|" + prefix},
						backing:     backing, prefix: prefix,
					}}
				}
				public := newSource("https://public.example", publicPrefix)
				private := newSource("https://private.example", privatePrefix)
				if layout == "distinct" {
					private.backing = make(map[string][]byte)
					// Matching bucket/prefix strings on a different store do not
					// make this ordinary public upload private.
					backing["branding/private/logo.webp"] = []byte("other logo")
				}
				backing[public.physical("branding/logo.webp")] = []byte("logo")
				private.backing[private.physical("diagnostics/report.zip")] = []byte("report")
				beforePublic, beforePrivate := public.view().objects, private.view().objects
				switch layout {
				case "probe_error":
					public.statErr = errors.New("source probe failed")
				case "cleanup_error":
					private.deleteErr = errors.New("source cleanup failed")
				}
				publicTarget := &memoryStore{identity: "s3|endpoint|new-public|", objects: make(map[string][]byte)}
				privateTarget := &memoryStore{identity: "s3|endpoint|new-private|", objects: make(map[string][]byte)}
				stage := stagedTarget{ID: "source-alias", Policy: policy, SourceIdentity: public.Identity(), Phase: transitionPhaseStaged, Values: map[string]string{settingArtworkBackend: blobstore.BackendS3, settingPrivateBucket: "new-private"}}
				raw, err := json.Marshal(stage)
				if err != nil {
					t.Fatal(err)
				}
				settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
				service := New(nil, settings, nil, public, private)
				service.openPublic = func(map[string]string) (blobstore.Store, error) { return publicTarget, nil }
				service.openPrivate = func(map[string]string) blobstore.Store { return privateTarget }
				_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: policy}, func(adminjob.StorageTransitionProgress) {})
				if (layout == "probe_error" || layout == "cleanup_error") && policy != PolicyFresh {
					if err == nil || !strings.Contains(err.Error(), "source") {
						t.Fatalf("expected source namespace probe error, got %v", err)
					}
					if len(publicTarget.objects) != 0 || len(privateTarget.objects) != 0 || settings.values[blobstore.IdentitySettingKey] != "" {
						t.Fatal("failed namespace probe allowed copying or settings commit")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantPublic, wantPrivate := map[string][]byte{}, map[string][]byte{}
				if policy != PolicyFresh {
					wantPublic["branding/logo.webp"] = []byte("logo")
					if layout == "distinct" {
						wantPublic["branding/private/logo.webp"] = []byte("other logo")
					}
				}
				if policy == PolicyMigrateAll {
					wantPrivate["diagnostics/report.zip"] = []byte("report")
				}
				if !reflect.DeepEqual(publicTarget.objects, wantPublic) || !reflect.DeepEqual(privateTarget.objects, wantPrivate) {
					t.Fatalf("objects routed incorrectly: public=%v private=%v", publicTarget.objects, privateTarget.objects)
				}
				if !reflect.DeepEqual(public.view().objects, beforePublic) || !reflect.DeepEqual(private.view().objects, beforePrivate) {
					t.Fatal("source objects changed or a probe was not removed")
				}
			})
		}
	}
}
