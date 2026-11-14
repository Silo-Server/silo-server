package historyimport

import (
	"errors"
	"testing"
)

func personalQueueRepository(t *testing.T) *Repository {
	t.Helper()
	repo := queueRunnerRepositoryFromPool(t, queueTestPoolBeforePersonalMigration(t, false))
	_, err := repo.pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY); INSERT INTO users VALUES(1),(2);
 CREATE TABLE user_profiles(user_id integer REFERENCES users(id),id text,PRIMARY KEY(user_id,id)); INSERT INTO user_profiles VALUES(1,'p'),(2,'other');
 ALTER TABLE history_import_runs ADD FOREIGN KEY(user_id,profile_id) REFERENCES user_profiles(user_id,id) ON DELETE CASCADE;
 CREATE TABLE history_import_connect_sessions(LIKE public.history_import_connect_sessions INCLUDING ALL);
 CREATE TABLE history_import_plex_sessions(LIKE public.history_import_plex_sessions INCLUDING ALL);
 INSERT INTO history_import_runs(id,status,connection_mode) VALUES('old-queued','queued','connect'),('old-running','running','connect'),('old-terminal','completed','connect')`)
	if err != nil {
		t.Fatal(err)
	}
	applyPersonalQueueMigration(t, repo.pool)
	return repo
}

// A personal import can name any server and the import slots are shared, so
// an account gets one queued or running personal import at a time.
func TestEnqueuePersonalRunAllowsOneActiveRunPerAccount(t *testing.T) {
	repo := personalQueueRepository(t)
	enqueue := func(userID int, profileID string) (*Run, error) {
		return repo.enqueuePersonalRun(t.Context(), personalRunAdmission{
			UserID: userID, ProfileID: profileID, SourceType: SourceTypeJellyfin, ConnectionMode: ConnectionModeCustom,
			Credentials: personalRunCredentials{BaseURL: "http://jellyfin.example", ExternalUserID: "external", ServerToken: "token"},
		})
	}

	first, err := enqueue(1, "p")
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := enqueue(1, "p"); !errors.Is(err, ErrPersonalRunActive) {
		t.Fatalf("second run while the first is queued: err = %v, want ErrPersonalRunActive", err)
	}
	if _, err := enqueue(2, "other"); err != nil {
		t.Fatalf("another account's run: %v", err)
	}
	if _, err := repo.pool.Exec(t.Context(), `UPDATE history_import_runs SET status='completed' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := enqueue(1, "p"); err != nil {
		t.Fatalf("run after the first finished: %v", err)
	}
}
