package executor

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const avatarBucket = "avatar-acceptance"

type avatarObjects struct {
	mu     sync.Mutex
	server *httptest.Server
	data   map[string]string
	calls  []string
}

// Local S3 protocol fixture used through the actual s3client. No external object
// credentials or shared bucket can enter this constructor.
func newAvatarObjects(t *testing.T) *avatarObjects {
	t.Helper()
	o := &avatarObjects{data: map[string]string{}}
	o.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.calls = append(o.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		if r.Method == http.MethodGet && r.URL.Path == "/"+avatarBucket && r.URL.Query().Get("list-type") == "2" {
			type item struct {
				Key  string
				Size int
			}
			out := struct {
				XMLName     xml.Name `xml:"ListBucketResult"`
				IsTruncated bool
				Contents    []item
			}{Contents: []item{}}
			for _, key := range slices.Sorted(maps.Keys(o.data)) {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					out.Contents = append(out.Contents, item{key, len(o.data[key])})
				}
			}
			w.Header().Set("Content-Type", "application/xml")
			if err := xml.NewEncoder(w).Encode(out); err != nil {
				t.Error(err)
			}
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/"+avatarBucket+"/") {
			key := strings.TrimPrefix(r.URL.Path, "/"+avatarBucket+"/")
			if _, ok := o.data[key]; !ok {
				http.Error(w, "missing object", 404)
				return
			}
			delete(o.data, key)
			w.WriteHeader(204)
			return
		}
		http.Error(w, "unexpected synthetic object request", 400)
	}))
	t.Cleanup(o.server.Close)
	return o
}
func (o *avatarObjects) snapshot() (map[string]string, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return maps.Clone(o.data), slices.Clone(o.calls)
}

func avatarDeleteOverlay(t *testing.T, e *Env, id string, o *avatarObjects) {
	t.Helper()
	for _, target := range []struct {
		user    int
		profile string
	}{{e.users[fixtureMember].ID, profilePrimary}, {e.users[fixtureMember].ID, profileSecondary}, {e.users[fixtureAdmin].ID, profileAdminPrimary}} {
		prefix := fmt.Sprintf("profile-avatars/%d/%s/", target.user, target.profile)
		o.data[prefix+"original.webp"] = "synthetic-original-" + target.profile
		o.data[prefix+"w256.webp"] = "synthetic-display-" + target.profile
		if id != "avatar_delete.meaning" || target.profile != profileSecondary {
			if _, err := e.pool.Exec(e.ctx, "UPDATE user_profiles SET avatar=$1 WHERE id=$2 AND user_id=$3", "upload:"+prefix+"original.webp", target.profile, target.user); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A prefix lookalike must survive the exact trailing-slash list boundary.
	prefix := fmt.Sprintf("profile-avatars/%d/%s-other/", e.users[fixtureMember].ID, profileSecondary)
	o.data[prefix+"original.webp"] = "unrelated-original"
	o.data[prefix+"w256.webp"] = "unrelated-display"
	if _, err := e.pool.Exec(e.ctx, "UPDATE user_profiles SET avatar='preset:fox' WHERE id=$1", profileChild); err != nil {
		t.Fatal(err)
	}
}

func (e *Env) checkAvatarEffects(t *testing.T, id string, before, after map[string]json.RawMessage, lower, upper time.Time, o *avatarObjects, objectBefore map[string]string, callsBefore []string) {
	t.Helper()
	mutate := id == "avatar_delete.ok" || id == "avatar_delete.shape" || id == "avatar_delete.any_profile"
	target := profileSecondary
	if id == "avatar_delete.any_profile" {
		target = profilePrimary
	}
	objectAfter, callsAfter := o.snapshot()
	if mutate {
		prefix := fmt.Sprintf("profile-avatars/%d/%s/", e.users[fixtureMember].ID, target)
		for _, key := range []string{prefix + "original.webp", prefix + "w256.webp"} {
			if _, ok := objectBefore[key]; !ok {
				t.Fatal("object fixture missing")
			}
			delete(objectBefore, key)
		}
		if len(callsAfter)-len(callsBefore) != 3 {
			t.Fatal("expected one actual S3 list and two object deletions")
		}
		var old, now []map[string]json.RawMessage
		if err := json.Unmarshal(before["profiles"], &old); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(after["profiles"], &now); err != nil {
			t.Fatal(err)
		}
		if len(old) != len(now) {
			t.Fatal("profile inventory changed")
		}
		matched := 0
		// Sort by profile identity; JSON snapshots otherwise sort by all fields.
		slices.SortFunc(old, func(a, b map[string]json.RawMessage) int { return bytes.Compare(a["id"], b["id"]) })
		slices.SortFunc(now, func(a, b map[string]json.RawMessage) int { return bytes.Compare(a["id"], b["id"]) })
		for i, row := range old {
			var pid string
			if err := json.Unmarshal(row["id"], &pid); err != nil {
				t.Fatal(err)
			}
			if pid != target {
				continue
			}
			matched++
			var priorAvatar, currentAvatar string
			if json.Unmarshal(row["avatar"], &priorAvatar) != nil || json.Unmarshal(now[i]["avatar"], &currentAvatar) != nil {
				t.Fatal("invalid avatar field")
			}
			if priorAvatar != "upload:"+prefix+"original.webp" || currentAvatar != "" {
				t.Fatal("uploaded avatar not cleared exactly")
			}
			var at time.Time
			if json.Unmarshal(now[i]["updated_at"], &at) != nil || at.Before(lower.Truncate(time.Second)) || at.After(upper.Truncate(time.Second)) {
				t.Fatal("profile timestamp outside writer second precision")
			}
			row["avatar"] = now[i]["avatar"]
			row["updated_at"] = now[i]["updated_at"]
		}
		if matched != 1 {
			t.Fatal("expected one target profile")
		}
		var err error
		before["profiles"], err = json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		after["profiles"], err = json.Marshal(now)
		if err != nil {
			t.Fatal(err)
		}
	} else if !reflect.DeepEqual(callsBefore, callsAfter) {
		t.Fatal("refusal/no-upload delete contacted object store")
	}
	if !reflect.DeepEqual(objectBefore, objectAfter) {
		t.Fatal("unexpected object bytes or inventory change")
	}
}
