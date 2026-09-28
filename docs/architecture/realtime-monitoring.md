# Real-time library monitoring

On Linux, Silo watches the folders of enabled libraries and scans only what
changed, usually within seconds of a file being added, finished, renamed, or
removed. It uses inotify. Network shares are not monitored. Each library
reports whether monitoring works and, when it doesn't, why.

Code lives in `internal/librarymonitor`. The v2 status and capability
operations are in `internal/apiv2/library_monitoring.go` and are described in
[libraries-api.md](../libraries-api.md#real-time-monitoring).

## Scope

- Monitoring is a library feature, not an autoscan source. The autoscan master
  switch does not gate it, and its scans carry their own trigger,
  `realtime_monitor` (labelled "File change" in the web scan views).
- Silo always scans the narrowest target: the file, its folder, or the vanished
  subtree. It widens to a whole-library scan only after a kernel event overflow
  or a burst of more than 1,000 targets for one library. There is no
  "full scan on change" mode.
- Changes made while Silo was down are not replayed. The initial walk of a
  folder queues no scans; the scheduled `scan_libraries` task (daily at 02:00 by
  default), a manual scan, or an autoscan source picks those changes up.
- Linux only. On other platforms every visible library folder reports
  `unsupported_platform` and nothing is watched.

## Switches and effective state

Two switches control monitoring:

- `scanner.realtime_monitoring`, the server-wide switch (default `true`). It
  hot-reloads through the config watcher and needs no restart.
- `media_folders.realtime_monitoring`, the per-library switch (default `true`
  for existing and new libraries). It is exposed on `/api/v2/libraries` only;
  the frozen `/api/v1` routes neither return nor change it.

A library is monitored only when the server switch is on, the library is
enabled, and its own switch is on. The monitor's desired set is exactly those
libraries, expanded to their folder paths. jellycompat lists only enabled
libraries, and for each one `EnableRealtimeMonitor` is true exactly when the
server switch and the library's own switch are on.

## Where it runs

The monitor runs in the `integrated` and `api` process modes, on nodes that
have a scan queue. Proxy and transcode nodes never run it.

Monitoring is node-local. Each node watches the library folders it can see and
sends the resulting scans to the shared scan queue. With local disks that is
one node in practice. Status is also per node:

- A node writes one row per library to `library_monitor_status`
  (`node_id`, `library_id`, `state`, `backend`, `detail`, `directories`,
  `updated_at`). Each write replaces all of that node's rows: rows for
  libraries it no longer reports are deleted, and rows are stamped with the
  database clock.
- It writes when a state changes and refreshes every 60 seconds.
- A library folder that does not exist on this node is skipped, unless this
  process monitored it earlier, in which case it reports `root_unavailable`. A
  node that can see none of a library's folders writes no row for it.
- On clean shutdown a node deletes its rows (best effort). A process that dies
  without running its shutdown path leaves rows that go stale after 3 minutes.

Readers ignore rows older than 3 minutes. The settings-derived states are never
stored; the status read computes them, in this order:

| State | When |
|---|---|
| `server_disabled` | The server switch is off. |
| `library_disabled` | The library is disabled. |
| `monitoring_off` | The library's switch is off. |
| `not_reporting` | No node has a fresh row ("No server node can see this library's folders"). |
| node report | The best fresh row: `monitoring`, then `starting`, `limit_reached`, `root_unavailable`, `unsupported_filesystem`, `unsupported_platform`, `error`. Ties go to the newer row, then the lower node ID. |

A stored state the reading build does not know is shown as `error` with its
detail kept.

Within one node, a library with several folders takes its worst folder's state:
`error` > `limit_reached` > `root_unavailable` > `unsupported_filesystem` >
`unsupported_platform` > `starting` > `monitoring`. The detail says each
distinct thing once: folders with the same detail are named together, and a
detail that every folder of the library shares, or the detail of a
single-folder library, carries no path. `directories` is the sum across
folders. When the folders use different backends, the row names `inotify` and
the detail carries each folder's fallback reason.

## Reconcile

The monitor re-reads the library list and diffs the desired folders against the
current ones:

- every 30 seconds, which also picks up library edits made on other nodes;
- immediately after a library create, update, or delete handled on this node;
- immediately when `scanner.realtime_monitoring` changes.

Each folder is checked in its own goroutine: `stat`, then `statfs`
classification, then backend selection and the initial walk. Startup never
waits for this. Walks run one at a time in library `sort_order`, so it is
predictable which libraries get watches when the inotify limit is tight. A walk
that records no new directory for 30 seconds stops holding later walks back, so
a hung mount cannot block other libraries. A folder removed from the desired
set mid-walk is cancelled and released.

Every reconcile also checks that a monitored folder still holds what was
walked:

- The folder's device and inode are compared with the ones recorded at the
  walk. A folder that was replaced, or that shows a different filesystem, is
  released and recorded from scratch, so the monitor never keeps watching an
  empty mountpoint.
- The mounts at or below the folder, and the mount that holds it, are read
  from `/proc/self/mountinfo` (mount ID, device, filesystem type, mount point)
  and compared with the set recorded before the walk. When a filesystem was
  mounted or unmounted inside the folder, the folder is walked again. Nothing
  is queued; changes made while the filesystem was away are the nightly
  scan's job.
- A quick `umount && mount` of the same device keeps the device, inode, fsid,
  and usually the mount ID (the kernel reuses the lowest free one), so the
  checks above cannot see it. The backends catch it instead: inotify sends
  `IN_UNMOUNT` for every watch on the unmounted filesystem, and a folder with
  one below it is walked again at once (a folder that was itself unmounted is
  lost, see [Folder loss](#folder-loss)).

A re-walk asked for while an attempt on the folder is running starts as soon
as that attempt ends.

Retry behaviour by state:

- `root_unavailable`, `unsupported_filesystem`, and `error` are retried on
  every reconcile.
- `limit_reached` is retried only when `fs.inotify.max_user_watches` changed or
  another folder released watches since the limit was hit, so a large tree is
  not re-walked every 30 seconds for nothing.
- `unsupported_platform` is never retried.

## Backend

The backend calls `golang.org/x/sys/unix` directly and turns kernel events
into one common form: directory, name, kind (create, close-write, moved from,
moved to, rename, delete), and whether the entry is a directory, plus
overflow, root lost, and limit reached. Everything after that (quiet window,
classification, scan resolution) works on that form. Paths are always built
from the configured library path, even when it or a directory below it is a
symlink, because the scan resolver matches configured paths.

The backend does not subscribe to `IN_MODIFY`. A copy always ends in a
close-write, so per-write events would only add volume.

- One inotify instance per process and one watch per recorded directory, with
  the mask `IN_CREATE | IN_CLOSE_WRITE | IN_MOVED_FROM | IN_MOVED_TO |
  IN_DELETE | IN_DELETE_SELF | IN_MOVE_SELF | IN_ONLYDIR | IN_EXCL_UNLINK`. The
  kernel adds `IN_IGNORED`, `IN_UNMOUNT`, and `IN_Q_OVERFLOW` on its own.
- A new directory gets its watch before it is listed, so a file created while
  the listing runs still produces an event. A recursive copy can write files
  into a new directory before its watch exists; the walk of a directory that
  was just created reports the files it finds (`Event.Found`), and the tracker
  holds the directory until they are complete (see
  [Event handling](#event-handling)). A directory moved in arrives whole, so
  its files are not reported.
- A directory reachable under several paths (overlapping library folders, or a
  symlink in one folder pointing into another) has one watch descriptor.
  Watches are reference-counted per folder and path, so removing one folder
  never drops a watch another folder still needs.
- A directory moved out of the tree or deleted has its whole recorded subtree
  dropped, so no event arrives under a stale path. A directory renamed inside
  the tree is re-recorded under its new paths; the kernel returns the same
  watch descriptors, so no watch is re-created.
- A symlink to a directory is followed and recorded like a directory, but the
  kernel does not flag its events as directory events. The backend treats a
  deleted or moved entry as a directory when a directory is recorded under its
  name, and a renamed or moved-in entry as one when it resolves to a
  directory, so renaming or deleting such a symlink moves or drops its watches
  like a real directory's.
- `IN_UNMOUNT` on a directory below a folder (a filesystem mounted inside it
  went away) asks the monitor to walk the folder again.
- `IN_MOVE_SELF` or `IN_DELETE_SELF` on a directory the walk reached through a
  symlink also asks for a re-walk: the link's target was moved or deleted where
  it really lives, which may be outside every watched directory, so no entry
  event reports it. The re-walk drops the stale path and records a target
  that is back in place. A target that returns later, with nothing watched
  seeing it, is picked up by the next re-walk or the nightly scan.
- A re-walk records every directory it reaches afresh. A path counts as a
  symlink alias only against paths the same walk recorded, so a directory
  renamed while its events were lost (an overflow) is recorded under its new
  name, and the walk's cleanup drops the old one.
- Move halves are paired by cookie. If a read ends between the two halves, the
  moved-from waits at most 20 ms for its partner before it counts as a move
  out. The wait is checked after every read, so a steady stream of other
  events cannot hold a move open, and at most 1,024 moved-from halves wait at
  once; older ones count as moves out.
- `inotify_add_watch` resolves a path and can block on a hung mount, so it runs
  outside the lock that status reads take.
- Limited by `fs.inotify.max_user_watches`; see
  [Watch limit](#watch-limit-inotify). Each watch pins its directory's inode in
  kernel memory, roughly 1 KB.

## Walk and ignore rules

The initial walk, and the walk of any directory that appears later, record the
directories the scanner would enter (`walkTree`). They skip:

- The fixed ignore list, matched case-insensitively against names:
  `*.partial`, `*.partial~` (Sonarr and Radarr copy imports), `*.part`,
  `*.tmp`, `*.!qB`, `@eaDir`, `#recycle`, `.recyclebin`, `.Trash-*`,
  `lost+found`. Files and directories with these names are also
  dropped from events before tracking, and a rename from an ignored name
  (`movie.mkv.part` to `movie.mkv`) counts as a move in.
- The scanner's other ignored directory names, also case-insensitive:
  `@Recycle`, `.Trash`, `$RECYCLE.BIN`, `.deleted`, `.inbound`, `.downloads`.
- A directory holding a regular `.nomedia` file, or a regular `.ignore` file
  without a valid pattern, is skipped with everything below it. The decision
  is the scanner's own (`scanner.DirSkipped`, see
  [scanner-ignore-files](scanner-ignore-files.md)), so monitoring and scans
  skip the same directories. The directory itself stays
  recorded (a library folder with such a marker holds one watch), so its
  markers keep being checked. Other changes in it are dropped. When a marker
  is deleted, moved away, or rewritten so it no longer excludes the directory,
  the directory is walked and reported as one folder change. For a library
  folder itself a whole-library scan is queued instead: a change to a file
  directly in the library folder resolves to a single-file scan, which does
  not apply the folder's own ignore rules. When a marker appears,
  everything recorded below the directory is dropped, except what a library
  folder configured below it records: markers above a library folder do not
  apply to it, as in the scanner. The directory is then reported the same way,
  so the scan retires what the marker now excludes. Marker files are never reported as changes
  themselves.
- Pattern rules inside `.ignore` and `.siloignore` are not applied to the
  walk. A pattern-ignored folder costs a watch, and a change there resolves to
  a scan that the scanner then filters. Creating, rewriting, or removing an
  `.ignore` or `.siloignore` in a monitored directory reports the directory for
  a scan (a whole-library scan for a library folder itself, as above), so
  entries its patterns now include or exclude are picked up; the files
  themselves are never reported.
- A walk lists a directory before applying what its ignore files say. If a
  marker changed in between and the event for it was handled first, the walk
  follows that newer answer instead of its own listing.
- Symlinked directories are followed, as the scanner follows them. Loops are
  cut by physical path within a walk, and across walks because a directory
  already recorded for the same folder under another path is not recorded
  again. So a directory can be recorded only through a symlink that sorts
  before its own path (`Linked -> Target`). When a path the walk reached
  through a symlink is dropped (the link, or a folder holding it, is
  deleted, moved out, or excluded by a marker), the folder is walked again,
  which records the directory under whatever path still reaches it.
- A network filesystem (NFS, SMB/CIFS, CephFS, 9p, by the mountinfo type)
  mounted below a folder is not entered, for the same reasons a network folder
  is not monitored. The walk finds such mounts in `/proc/self/mountinfo`
  without touching them, so a hung one cannot stall it.
- A symlink whose target is on such a filesystem is not followed either,
  whether it exists when the folder is walked or is created later. The link
  text is checked against the mount table before the target is touched, so a
  direct link into a hung mount never blocks the walk or the event loop. A
  target reached through a chain of links is only caught after resolving the
  chain, which does touch the mount. A link created later is reported as a
  file change, so the scanner scans it once.
- "On such a filesystem" means the mount that actually holds the path, the
  deepest mount point above it: a local disk mounted inside a network share is
  monitored.
- The folder's status detail names skipped mounts and symlinks, up to three
  and a count of the rest ("Folders on network filesystems aren't monitored:
  …"). Changes made there by other machines produce no events, so they stay
  with arr webhooks, the CephFS autoscan source, or the nightly scan. The
  library still reports `monitoring`, because its own folder is monitored,
  even when every entry is such a link; only the detail says what is left
  out.
- A directory Silo may not read cannot be watched (`inotify_add_watch` fails
  with `EACCES` or `EPERM`); the scanner cannot read it either. Below a
  library folder it is skipped with everything under it, and the folder's
  status detail names it ("Folders Silo can't read aren't monitored: …") so
  the status never claims it. Nothing reports a permission change on a
  directory that is not watched, so every reconcile checks whether such a
  directory can be opened now and walks the folder again when one can. A
  directory that appears unreadable at runtime asks for a walk at once. A
  library folder Silo cannot watch, or any other kernel error, fails the walk,
  and the folder shows `error` with the reason. Only a directory that vanished
  or stopped being a directory after it was listed (`ENOENT`, `ENOTDIR`) is
  skipped silently.
- A directory that can be watched but not listed stays recorded but is not
  descended into.

## Event handling

Events are tracked per path. A path is reported once it is **complete and
quiet for 5 seconds**; another event on the same path restarts the window.

| Event | Meaning | Reported as |
|---|---|---|
| `IN_CLOSE_WRITE` | A writer finished | File change |
| `IN_MOVED_TO`, file | Moved or renamed in (arr move imports, rsync finishing) | File change |
| `IN_CREATE`, file with link count > 1 | Hardlink (arr import when downloads share the filesystem) | File change |
| `IN_CREATE`, symlink | Symlink placed | File change |
| `IN_CREATE`, file with link count 1 | A copy has started | Waits for `IN_CLOSE_WRITE`. Without one after 2 minutes, the file is checked every 30 seconds and reported once two checks agree on size and mtime. |
| File found by the walk of a new directory, link count 1 | Possibly written before the directory was watched | Complete at its `IN_CLOSE_WRITE`, or once its size and mtime hold across one quiet window; the new directory waits for it. |
| `IN_CREATE` / `IN_MOVED_TO`, directory | New or moved-in folder | The backend records the subtree first. One subtree change for the folder, held while anything below it is still incomplete; it absorbs the changes below it. |
| `IN_DELETE` / `IN_MOVED_FROM`, file | Removed or moved out | Vanished-file change, unless the file was created (`IN_CREATE`) since the last report: then it never existed for the catalog and is dropped. That covers rsync's and downloaders' temp names renamed into place. A moved-in file is not dropped this way, because a move can replace an existing file. |
| `IN_DELETE` / `IN_MOVED_FROM`, directory | Removed or moved out | Vanished-subtree change; pending changes below it are dropped |
| Move pair inside the tree | Rename | Vanished change for the old path (dropped as above for a path created since the last report) plus a change for the new one. For a directory, pending changes below it move to the new path. |
| `IN_Q_OVERFLOW` | The kernel dropped events | See [Overflow](#overflow) |
| Root deleted, moved, or unmounted | Folder gone | See [Folder loss](#folder-loss) |

## From changes to scans

Every 2 seconds the monitor flushes reported changes:

1. Each change is resolved with `scantrigger.Resolver`, the resolver autoscan
   uses, over the library list from the last reconcile, so a burst of changes
   costs no database query per path. A library created or edited on this node
   is listed again at once; one edited on another node within the reconcile
   interval. The resolver uses `Resolve` for present files and folders, `ResolveVanishedPath` for
   vanished files, `ResolveMissingSubtree` for vanished folders. The vanished
   resolvers refuse to queue cleanup while the library's root is missing, so a
   lost mount never turns into removals. The scanner still treats a folder
   that no longer exists as unreadable, so the files of a renamed or deleted
   folder stay cataloged, and the library shows a partial-scan warning, until
   the next full library scan marks them missing. Deleted files are
   reconciled at once, because their folder still exists.
2. A `scantrigger.RequestError` (a sidecar file, a path that vanished again, an
   offline root) is an expected skip. Other resolve errors are retried once on
   the next flush, then dropped with a warning.
3. A single change that resolves to a whole-library target is dropped, and so
   is a target whose library no longer has monitoring on.
4. Targets are deduplicated. A library with more than 1,000 targets in one
   flush (the same cap as autoscan's per-poll limit) collapses to one library
   scan, and a library that already has a library scan in the batch drops its
   narrower targets.
5. `EnqueueScans` sends the batch with trigger `realtime_monitor`. A failed
   enqueue is retried once.

There is no Redis suppression. The quiet window and the scan queue's
coalescing absorb bursts: the queue reuses an accepted run for the same scope,
and records one follow-up scan for a scope that is mid-scan. If an arr webhook
and the monitor report the same import, the queue merges them.

## Overflow

When the kernel reports a queue overflow, the monitor re-walks every monitored
folder, to record directories created in the gap, and queues one
whole-library scan for each library with a monitored folder. A folder whose
first walk is still running counts too: its library scan is queued at once, and the
folder is walked again as soon as the first walk ends.

## Backend failure

If the backend's event stream ends while the monitor is running (its reader
hit an unexpected read error), nothing it records is watched any more. The
monitor logs an error, drops that backend, marks its folders `starting`, and
reconciles at once: a new inotify instance records every folder again. Changes
made in the gap are the nightly scan's job.

## Folder loss

When a library folder is deleted, moved, or unmounted, its state becomes
`root_unavailable`, everything recorded for it is released, pending changes
below it are dropped, and nothing is queued. A folder nested inside another
monitored folder is detected as lost through its parent's event. Reconcile
retries every 30 seconds.

A backend that releases a folder while the folder's walk is still running
makes that walk fail instead of succeed, so the folder is never reported as
monitored with nothing recorded.

## Watch limit (inotify)

- If adding a watch fails with `ENOSPC`, that folder's state becomes
  `limit_reached` and all its watches are released, so the status never claims
  coverage with holes. Other folders keep going.
- The detail reports the folder's directory count and the current
  `fs.inotify.max_user_watches`, says the limit must be raised on the host
  (containers can't change it).
- After an initial walk hits the limit, the walk keeps counting without adding
  watches, so the directory count is the full tree. After a runtime hit (a new
  subtree), the count is the watches held at that moment, a lower bound.
- A runtime hit can arrive while the folder is being walked (its first walk, or
  a re-walk after an overflow). The walk then fails with the limit, and the
  monitor keeps `limit_reached` instead of the walk's result.
- Walks run in library `sort_order`, so the libraries listed first win when the
  limit is tight.
- Tests inject `ENOSPC` through a seam. Nothing in Silo or its tests changes
  host sysctls.

## Filesystem support

Each folder is classified with `statfs` `f_type`:

| Filesystem | Behaviour |
|---|---|
| Local (ext4, xfs, btrfs, ZFS, …) | Monitored |
| FUSE (mergerfs, Unraid user shares, virtiofs on Docker Desktop) | Monitored, with the note "FUSE filesystem: changes made outside this mount (for example directly on a pool member disk) aren't seen." |
| NFS, SMB, CIFS, SMB2, CephFS | `unsupported_filesystem`. Writes from other machines produce no events, and a hung network mount would stall the walk. The detail points to arr webhooks, the CephFS autoscan source, or the nightly scan. |
| 9p (WSL `/mnt/c`, some VM shares) | `unsupported_filesystem`: host-side changes produce no events. |

## What was verified

A sandbox deployment on Linux 6.8 with a local ext4 library passed copying a
file, a hardlink import, a 29-second slow copy (no scan until the file
closed), a folder rename, a deletion, a temp-file-then-rename write, a move-in,
series episodes, both switches, and a library folder removed and restored.

## Operator guidance

If a library reports the watch limit, raise `fs.inotify.max_user_watches` on
the host. Keep library folders on bind mounts or host filesystems, not inside a
container's own filesystem.

### Raise the inotify watch limit

The limit is a host setting. A container cannot change it, and without user
namespaces every process running as the same host UID shares one budget,
across all containers. Other apps that use inotify (Plex, Jellyfin, Syncthing,
IDEs) draw from the same budget when they run as that UID.

```ini
# /etc/sysctl.d/60-silo-inotify.conf
fs.inotify.max_user_watches = 1048576
```

Apply it with `sudo sysctl --system`. Silo notices the new value on its next
reconcile and retries libraries in `limit_reached`. On TrueNAS SCALE, add the
same variable under System Settings → Advanced → Sysctl; on Unraid, the Tips
and Tweaks plugin sets it.

## Known limitations

- Directories created at runtime are recorded on the backend's read loop. A
  symlink inside a library that points at a hung local or FUSE mount would
  stall event reading for every folder. Direct links onto
  network filesystems are skipped without being touched; a chain of links
  into one is resolved first.
- The monitor's event loop stats created files, releases the watches of
  library folders removed from the desired set, and the flush's resolver stats
  changed paths. A hung FUSE mount (a dead mergerfs
  branch) can hold that loop and delay changes for every library until the
  call returns. Releases never run under the monitor's lock, so status reports
  keep going. Shutdown does not wait for a stuck loop: `Stop` gives it 5
  seconds, then goes on.
- The mount check only covers mounts below a folder's physical path. A local
  filesystem reached through a symlink inside a library is not re-walked when
  it is remounted, until an overflow or a restart.
- Library deletes and edits made through another node reach this node's
  monitor only on the 30-second reconcile.
