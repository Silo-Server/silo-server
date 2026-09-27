package plugins

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"fmt"
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
	if f, err := elf.Open(path); err == nil {
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
	if f, err := macho.Open(path); err == nil {
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
	if f, err := pe.Open(path); err == nil {
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

// checkBinaryPlatform fails when the binary at path was built for another
// platform than this process runs on. The archive in plugin_archives was
// resolved for the API server's platform at install time, so a proxy node
// on a different platform cannot run it; a clear error here beats the
// supervisor discovering an exec failure on every restart.
func checkBinaryPlatform(path string) error {
	goos, goarch, ok := binaryPlatform(path)
	if !ok {
		return nil
	}
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		return fmt.Errorf("plugin binary is built for %s/%s but this host is %s/%s; the stored archive was resolved for the API server's platform, so every proxy node must run the same platform in this release", goos, goarch, runtime.GOOS, runtime.GOARCH)
	}
	return nil
}
