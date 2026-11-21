// Command forkpins checks the fork replacements against the required shunpike release.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
)

const shunpikePath = "github.com/darthShadow/shunpike"

var forkPaths = [...]string{"github.com/quic-go/quic-go", "tailscale.com"}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	server, err := os.ReadFile("go.mod")
	if err != nil {
		return fmt.Errorf("read silo-server go.mod: %w", err)
	}
	file, err := modfile.Parse("silo-server go.mod", server, nil)
	if err != nil {
		return err
	}
	pin, err := shunpikePin(file)
	if err != nil {
		return err
	}
	for _, path := range forkPaths {
		if _, err := forkReplacement(file, path); err != nil {
			return fmt.Errorf("silo-server: %w", err)
		}
	}
	out, err := exec.CommandContext(ctx, "go", "mod", "download", "-json", pin.String()).CombinedOutput() //nolint:gosec // The parsed module pin uses the checked org path and no shell.
	if err != nil {
		return fmt.Errorf("download %s: %w\n%s", pin, err, out)
	}
	var downloaded struct {
		GoMod string
		Error string
	}
	if err := json.Unmarshal(out, &downloaded); err != nil {
		return fmt.Errorf("decode download for %s: %w", pin, err)
	}
	if downloaded.Error != "" || downloaded.GoMod == "" {
		return fmt.Errorf("download %s returned no usable go.mod: %s", pin, downloaded.Error)
	}
	upstream, err := os.ReadFile(downloaded.GoMod)
	if err != nil {
		return fmt.Errorf("read shunpike go.mod: %w", err)
	}
	if err := compareForkPins(server, upstream); err != nil {
		return err
	}
	fmt.Printf("Fork pins match %s\n", pin)
	return nil
}

func shunpikePin(file *modfile.File) (module.Version, error) {
	var version string
	for _, required := range file.Require {
		if required.Mod.Path == shunpikePath {
			version = required.Mod.Version
			break
		}
	}
	if version == "" {
		return module.Version{}, fmt.Errorf("silo-server must require %s", shunpikePath)
	}
	return module.Version{Path: shunpikePath, Version: version}, nil
}

func forkReplacement(file *modfile.File, path string) (*modfile.Replace, error) {
	var found *modfile.Replace
	for _, replacement := range file.Replace {
		if replacement.Old.Path != path {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%s: multiple replace directives", path)
		}
		found = replacement
	}
	if found == nil {
		return nil, fmt.Errorf("%s: missing replace directive", path)
	}
	if found.New.Version == "" {
		return nil, fmt.Errorf("%s: local directory replacements are not release pins", path)
	}
	return found, nil
}

func compareForkPins(server, upstream []byte) error {
	serverFile, err := modfile.Parse("silo-server go.mod", server, nil)
	if err != nil {
		return err
	}
	if _, err := shunpikePin(serverFile); err != nil {
		return err
	}
	upstreamFile, err := modfile.Parse("shunpike go.mod", upstream, nil)
	if err != nil {
		return err
	}
	for _, path := range forkPaths {
		serverPin, err := forkReplacement(serverFile, path)
		if err != nil {
			return fmt.Errorf("silo-server: %w", err)
		}
		upstreamPin, err := forkReplacement(upstreamFile, path)
		if err != nil {
			return fmt.Errorf("shunpike: %w", err)
		}
		if serverPin.Old != upstreamPin.Old || serverPin.New != upstreamPin.New {
			return fmt.Errorf("%s: silo-server replace %s => %s differs from shunpike replace %s => %s",
				path, serverPin.Old, serverPin.New, upstreamPin.Old, upstreamPin.New)
		}
	}
	return nil
}
