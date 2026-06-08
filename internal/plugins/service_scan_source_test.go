package plugins

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

func TestScanSourceClientByPluginIDResolvesCurrentInstallation(t *testing.T) {
	manifest := testPluginManifest(t, "silo.autoscan.cephfs", "0.2.2")
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{
		{Type: "scan_source.v1", Id: "cephfs", DisplayName: "CephFS"},
	}
	installPath := writeInstalledPluginManifest(t, manifest)
	host := &fakeServiceHost{
		clientErr:   pluginhost.ErrClientNotFound,
		startResult: &fakePluginClient{manifest: manifest},
	}
	service := &Service{
		installations: &fakeServiceInstallationStore{
			byID: map[int]*Installation{
				8: {
					ID:          8,
					PluginID:    "silo.autoscan.cephfs",
					Version:     "0.2.2",
					InstallPath: installPath,
					Enabled:     true,
				},
			},
			byPluginID: map[string][]*Installation{
				"silo.autoscan.cephfs": {
					{ID: 8, PluginID: "silo.autoscan.cephfs", Version: "0.2.2", InstallPath: installPath, Enabled: true},
				},
			},
			listCapabilities: []*Capability{{InstallationID: 8, Type: "scan_source.v1", ID: "cephfs"}},
		},
		host: host,
	}

	if _, err := service.ScanSourceClientByPluginID(context.Background(), "silo.autoscan.cephfs", "cephfs"); err != nil {
		t.Fatalf("ScanSourceClientByPluginID: %v", err)
	}
	if len(host.started) != 1 || host.started[0].InstallationID != 8 {
		t.Fatalf("started installations = %+v, want installation 8", host.started)
	}
}

func TestScanSourceClientByPluginIDRejectsAmbiguousInstallations(t *testing.T) {
	manifest := testPluginManifest(t, "silo.autoscan.cephfs", "0.2.2")
	manifest.Capabilities = []*pluginv1.CapabilityDescriptor{
		{Type: "scan_source.v1", Id: "cephfs", DisplayName: "CephFS"},
	}
	installPath := writeInstalledPluginManifest(t, manifest)
	service := &Service{
		installations: &fakeServiceInstallationStore{
			byID: map[int]*Installation{
				8: {ID: 8, PluginID: "silo.autoscan.cephfs", Version: "0.2.2", InstallPath: installPath, Enabled: true},
				9: {ID: 9, PluginID: "silo.autoscan.cephfs", Version: "0.2.3", InstallPath: installPath, Enabled: true},
			},
			byPluginID: map[string][]*Installation{
				"silo.autoscan.cephfs": {
					{ID: 8, PluginID: "silo.autoscan.cephfs", Version: "0.2.2", InstallPath: installPath, Enabled: true},
					{ID: 9, PluginID: "silo.autoscan.cephfs", Version: "0.2.3", InstallPath: installPath, Enabled: true},
				},
			},
			listCapabilities: []*Capability{{Type: "scan_source.v1", ID: "cephfs"}},
		},
		host: &fakeServiceHost{},
	}

	_, err := service.ScanSourceClientByPluginID(context.Background(), "silo.autoscan.cephfs", "cephfs")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ScanSourceClientByPluginID error = %v, want ambiguous", err)
	}
}
