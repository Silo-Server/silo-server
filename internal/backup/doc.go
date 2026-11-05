// Package backup takes, lists, rotates, and restores whole-server backups:
// a pg_dump of the database plus the blobs that cannot be fetched again
// (uploaded artwork, collection images, library posters, branding, avatars,
// downloaded subtitles). docs/architecture/backups.md is the durable
// description; this comment states only the rules code must keep.
//
// # Layout
//
// Every backup is one folder at the destination:
//
//	backups/<server-id>/<UTC time>-<kind>/
//	  db.dump      pg_dump custom format, zstd
//	  blobs.tar    assets/<key> and operational/<key> entries
//	  restore-set.tar the sections selective restore reads
//	  manifest.json written last
//
// A folder without manifest.json is incomplete. The sweep deletes incomplete
// folders older than 24 hours; nothing else ever reads one.
//
// # Locks
//
// One cluster-wide lock covers the subsystem: a PostgreSQL session advisory
// lock (SubsystemLockKey) on a dedicated connection. Every operation that
// writes or deletes backups, or restores from one, holds it: the backup
// task, "Back up now", rotation, deletion, and the incomplete-folder sweep.
// Rotation and the sweep run inside the backup that holds the lock, so they
// never take it a second time. A lost lock session cancels the work holding
// it; the incomplete folder it leaves is the sweep's.
//
// A restore holds the subsystem lock for its whole run and takes its
// before-restore backup by calling Backup directly, never by acquiring the
// lock again.
package backup
