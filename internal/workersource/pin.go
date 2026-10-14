package workersource

import (
	"os"
	"reflect"
)

// FileIdentity is meaningful only on its source owner. It is not a whole-file
// digest and cannot establish byte identity between different storage owners.
type FileIdentity struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Size      int64  `json:"size"`
	MTimeNano int64  `json:"mtime_nano"`
	CTimeNano int64  `json:"ctime_nano"`
	Mode      uint32 `json:"mode"`
}

func identityOf(info os.FileInfo) (FileIdentity, error) {
	if !info.Mode().IsRegular() {
		return FileIdentity{}, ErrDenied
	}
	v := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return FileIdentity{}, ErrIdentityUnavailable
	}
	dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
	if !dev.IsValid() || !ino.IsValid() || !dev.CanUint() || !ino.CanUint() {
		return FileIdentity{}, ErrIdentityUnavailable
	}
	ctime := v.FieldByName("Ctim")
	if !ctime.IsValid() {
		ctime = v.FieldByName("Ctimespec")
	}
	if !ctime.IsValid() || ctime.Kind() != reflect.Struct {
		return FileIdentity{}, ErrIdentityUnavailable
	}
	sec, nsec := ctime.FieldByName("Sec"), ctime.FieldByName("Nsec")
	if !sec.IsValid() || !nsec.IsValid() || !sec.CanInt() || !nsec.CanInt() {
		return FileIdentity{}, ErrIdentityUnavailable
	}
	return FileIdentity{Device: dev.Uint(), Inode: ino.Uint(), Size: info.Size(), MTimeNano: info.ModTime().UnixNano(), CTimeNano: sec.Int()*1e9 + nsec.Int(), Mode: uint32(info.Mode())}, nil
}

func openPin(path string) (*os.File, FileIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, FileIdentity{}, ErrUnavailable
	}
	if !info.Mode().IsRegular() {
		return nil, FileIdentity{}, ErrDenied
	}
	f, err := openSourceFile(path)
	if err != nil {
		return nil, FileIdentity{}, ErrUnavailable
	}
	info, err = f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, FileIdentity{}, ErrUnavailable
	}
	identity, err := identityOf(info)
	if err != nil {
		_ = f.Close()
		return nil, FileIdentity{}, err
	}
	if err := validatePin(f, path, identity); err != nil {
		_ = f.Close()
		return nil, FileIdentity{}, err
	}
	return f, identity, nil
}

func validatePin(f *os.File, path string, pin FileIdentity) error {
	for _, get := range []func() (os.FileInfo, error){f.Stat, func() (os.FileInfo, error) { return os.Stat(path) }} {
		info, err := get()
		if err != nil {
			return ErrUnavailable
		}
		identity, err := identityOf(info)
		if err != nil {
			return err
		}
		if identity != pin {
			return ErrChanged
		}
	}
	return nil
}
