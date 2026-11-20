package plugins

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
	"io"
	"os"
	"runtime"
)

const (
	archAMD64 = "amd64"
	archARM64 = "arm64"
	osLinux   = "linux"
)

// binaryPlatform reports the GOOS/GOARCH a plugin binary was built for, read
// from its executable header. ok is false for formats this host does not
// recognize; callers treat that as "unknown" rather than as a mismatch.
func binaryPlatform(path string) (goos, goarch string, ok bool) {
	file, err := os.Open(path) //nolint:gosec // the plugin cache's own binary path, which the caller derived.
	if err != nil {
		return "", "", false
	}
	defer func() { _ = file.Close() }()
	return binaryPlatformOf(file)
}

// binaryPlatformOf is binaryPlatform for a binary read through r.
func binaryPlatformOf(r io.ReaderAt) (goos, goarch string, ok bool) {
	if f, err := elf.NewFile(r); err == nil {
		defer func() { _ = f.Close() }()
		switch f.Machine {
		case elf.EM_X86_64:
			goarch = archAMD64
		case elf.EM_AARCH64:
			goarch = archARM64
		case elf.EM_386:
			goarch = "386"
		case elf.EM_ARM:
			goarch = "arm"
		case elf.EM_RISCV:
			goarch = "riscv64"
		default:
			return "", "", false
		}
		// Go tags ELF binaries for linux and freebsd alike; linux is the
		// only ELF platform Silo ships plugins for.
		return osLinux, goarch, true
	}
	if f, err := macho.NewFile(r); err == nil {
		defer func() { _ = f.Close() }()
		switch f.Cpu {
		case macho.CpuAmd64:
			goarch = archAMD64
		case macho.CpuArm64:
			goarch = archARM64
		default:
			return "", "", false
		}
		return "darwin", goarch, true
	}
	if f, err := pe.NewFile(r); err == nil {
		defer func() { _ = f.Close() }()
		switch f.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			goarch = archAMD64
		case pe.IMAGE_FILE_MACHINE_ARM64:
			goarch = archARM64
		case pe.IMAGE_FILE_MACHINE_I386:
			goarch = "386"
		default:
			return "", "", false
		}
		return "windows", goarch, true
	}
	return "", "", false
}

// foreignBinaryError is a plugin binary built for another platform than
// this process runs on.
type foreignBinaryError struct{ goos, goarch string }

func (e *foreignBinaryError) Error() string {
	return fmt.Sprintf("plugin binary is built for %s/%s but this host is %s/%s", e.goos, e.goarch, runtime.GOOS, runtime.GOARCH)
}

// proxyPlatformNote explains a foreign binary on a proxy node, which has no
// catalog to fetch its own platform's build from.
const proxyPlatformNote = "the stored archive was resolved for the API server's platform, so every proxy node must run the same platform in this release"

// checkBinaryPlatform returns a *foreignBinaryError when the binary at path
// was built for another platform than this process runs on, so a clear error
// or a fetch of this platform's build replaces an exec failure on every
// start.
func checkBinaryPlatform(path string) error {
	goos, goarch, ok := binaryPlatform(path)
	if !ok {
		return nil
	}
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		return &foreignBinaryError{goos: goos, goarch: goarch}
	}
	return nil
}
