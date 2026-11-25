package main

import (
	"strings"
	"testing"
)

const pinnedModules = `module example.org/server

go 1.27.3

require github.com/darthShadow/shunpike v0.1.0

replace github.com/quic-go/quic-go => github.com/darthShadow/quic-go v0.64.1-patch.1
replace tailscale.com => github.com/darthShadow/tailscale v1.104.2-patch.1
replace github.com/zishang520/webtransport-go => ./internal/compat/shim
`

const (
	quic      = "replace github.com/quic-go/quic-go => github.com/darthShadow/quic-go v0.64.1-patch.1\n"
	tailscale = "replace tailscale.com => github.com/darthShadow/tailscale v1.104.2-patch.1\n"
)

var forkPinCases = []struct {
	name     string
	server   string
	upstream string
	wantErr  string
}{
	{name: "shunpike required without replace, forks equal", server: pinnedModules, upstream: pinnedModules},
	{
		name: "shim replace differs and is ignored", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, "./internal/compat/shim", "./another/shim"),
	},
	{
		name: "shunpike need not require itself", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, "require github.com/darthShadow/shunpike v0.1.0\n", ""),
	},
	{
		name: "quic missing in silo", server: strings.ReplaceAll(pinnedModules, quic, ""),
		upstream: pinnedModules, wantErr: "github.com/quic-go/quic-go",
	},
	{
		name: "tailscale missing in silo", server: strings.ReplaceAll(pinnedModules, tailscale, ""),
		upstream: pinnedModules, wantErr: "tailscale.com",
	},
	{
		name: "quic missing in shunpike", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, quic, ""), wantErr: "github.com/quic-go/quic-go",
	},
	{
		name: "tailscale missing in shunpike", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, tailscale, ""), wantErr: "tailscale.com",
	},
	{
		name: "new version differs", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, "v0.64.1-patch.1", "v0.64.1-patch.2"),
		wantErr:  "github.com/quic-go/quic-go",
	},
	{
		name:     "silo fork replace at a different version",
		server:   strings.ReplaceAll(pinnedModules, "v1.104.2-patch.1", "v1.104.2-patch.2"),
		upstream: pinnedModules, wantErr: "tailscale.com",
	},
	{
		name: "old version differs", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, "quic-go =>", "quic-go v0.64.1 =>"),
		wantErr:  "github.com/quic-go/quic-go",
	},
	{
		name: "new path differs", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, "github.com/darthShadow/tailscale", "github.com/other/tailscale"),
		wantErr:  "tailscale.com",
	},
	{
		name: "local directory in silo", server: strings.ReplaceAll(pinnedModules, "github.com/darthShadow/quic-go v0.64.1-patch.1", "../quic-go"),
		upstream: pinnedModules, wantErr: "local directory",
	},
	{
		name: "local directory in shunpike", server: pinnedModules,
		upstream: strings.ReplaceAll(pinnedModules, "github.com/darthShadow/tailscale v1.104.2-patch.1", "../tailscale"),
		wantErr:  "local directory",
	},
	{
		name:     "matching local directories still refused",
		server:   strings.ReplaceAll(pinnedModules, "github.com/darthShadow/quic-go v0.64.1-patch.1", "../quic-go"),
		upstream: strings.ReplaceAll(pinnedModules, "github.com/darthShadow/quic-go v0.64.1-patch.1", "../quic-go"),
		wantErr:  "local directory",
	},
	{
		name: "shunpike not required", server: strings.ReplaceAll(pinnedModules, "require github.com/darthShadow/shunpike v0.1.0\n", ""),
		upstream: pinnedModules, wantErr: "require github.com/darthShadow/shunpike",
	},
	{
		name: "ambiguous fork replacement", server: pinnedModules + "replace tailscale.com v1.104.2 => github.com/darthShadow/tailscale v1.104.2-patch.1\n",
		upstream: pinnedModules, wantErr: "tailscale.com",
	},
	{
		name: "shunpike pins a fork the server lacks", server: pinnedModules,
		upstream: pinnedModules + "replace golang.zx2c4.com/wireguard => github.com/darthShadow/wireguard v0.0.1-patch.1\n",
		wantErr:  "golang.zx2c4.com/wireguard",
	},
	{
		name: "server replaces shunpike", server: pinnedModules + "replace github.com/darthShadow/shunpike => ../shunpike\n",
		upstream: pinnedModules, wantErr: "must not replace github.com/darthShadow/shunpike",
	},
	{
		name: "malformed server module", server: "not a module file",
		upstream: pinnedModules, wantErr: "silo-server go.mod",
	},
	{
		name: "malformed shunpike module", server: pinnedModules,
		upstream: "not a module file", wantErr: "shunpike go.mod",
	},
}

func TestCompareForkPins(t *testing.T) {
	for _, tc := range forkPinCases {
		t.Run(tc.name, func(t *testing.T) {
			err := compareForkPins([]byte(tc.server), []byte(tc.upstream))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("compareForkPins: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("compareForkPins error = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}
