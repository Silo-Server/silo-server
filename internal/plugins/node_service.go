package plugins

import "context"

// NewNodeService builds the plugin service a proxy node runs. A proxy hosts
// the same network access provider installations as the API server, each
// with its own overlay identity, and nothing else: no catalog, no installer,
// no admin routes, no metadata or other capability dispatch. Everything the
// returned service can do is what the resident supervisor and the node's
// network-access routes need: read enabled resident installations, rehydrate
// their archives from plugin_archives into cacheDir (this node's own plugin
// cache root; the install paths the API server recorded belong to its
// filesystem, not this one), launch them with their runtime configuration,
// and drive Connect/Disconnect/GetStatus.
//
// Lifecycle mutations still happen on the API server; the node learns about
// them through FollowLifecycleChanges.
func NewNodeService(installations *InstallationStore, configs *RuntimeConfigStore, host Host, cacheDir string) *Service {
	svc := &Service{
		installations: installations,
		configs:       configs,
		archiveCache:  NewArchiveCacheAt(installations, cacheDir),
		host:          host,
	}
	svc.AddLifecycleHook(func(context.Context) { svc.invalidateInstallationCache() })
	svc.resident = newResidentSupervisor(svc, ResidentOptions{})
	svc.AddLifecycleHook(func(ctx context.Context) { svc.resident.Reconcile(ctx) })
	return svc
}
