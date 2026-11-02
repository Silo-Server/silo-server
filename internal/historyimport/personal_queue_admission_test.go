package historyimport

import (
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
