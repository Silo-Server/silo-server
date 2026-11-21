package envutil

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

// SecretFileSuffix names the variant of a secret variable that holds the path
// of a file to read the secret from, as Docker and Kubernetes mount secrets:
// SECRET_KEY_FILE for SECRET_KEY. Only secret variables have one.
const SecretFileSuffix = "_FILE"

// maxFileBytes bounds a file read from a path the environment names, so a
// mistaken path cannot make startup read a large file into memory.
const maxFileBytes = 1 << 20

// LookupSecret returns the secret variable name's value, read from the file
// that name+"_FILE" names when that is set instead. usedVar is the variable
// that supplied the value: name+"_FILE" for a file, otherwise name. A
// variable counts as set when it is not empty, as the omnibus image's init
// scripts count it.
// Setting both is an error, and so is a file that cannot be read or holds
// only whitespace. A value set directly is returned unchanged.
func LookupSecret(getenv func(string) string, name string) (value, usedVar string, err error) {
	fileVar := name + SecretFileSuffix
	path := getenv(fileVar)
	direct := getenv(name)
	switch {
	case path != "" && direct != "":
		return "", "", fmt.Errorf("set only one of %s and %s", name, fileVar)
	case path != "":
		secret, err := ReadSecretFile(strings.TrimSpace(path))
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", fileVar, err)
		}
		if strings.TrimSpace(secret) == "" {
			return "", "", fmt.Errorf("%s: the file holds only whitespace", fileVar)
		}
		return secret, fileVar, nil
	default:
		return direct, name, nil
	}
}

// trimOneLineEnding removes one trailing LF (and a preceding CR) from s,
// since editors and `echo` add one; anything else in s stays intact.
func trimOneLineEnding(s string) string {
	if line, ended := strings.CutSuffix(s, "\n"); ended {
		return strings.TrimSuffix(line, "\r")
	}
	return s
}

// readBounded reads at most maxFileBytes from r, refusing larger input so a
// mistaken path or stream cannot pull an unbounded body into memory.
func readBounded(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		return nil, errors.New("could not read file within the 1 MiB limit")
	}
	return data, nil
}

// ReadSecretFile reads a secret from a mounted file. One trailing line ending
// is removed, since editors and `echo` add one; anything else in the file is
// the secret. The file must be a regular file of at most 1 MiB holding a
// nonempty UTF-8 value. Errors never quote the file's content.
func ReadSecretFile(path string) (string, error) {
	data, err := ReadRegularFile(path)
	if err != nil {
		return "", err
	}
	secret := trimOneLineEnding(string(data))
	if secret == "" || !utf8.ValidString(secret) {
		return "", errors.New("the file must hold a nonempty UTF-8 value")
	}
	return secret, nil
}

// ReadRegularFile reads a regular file of at most maxFileBytes from a path
// the deployment chose. A FIFO or device is refused before it is opened, so it
// cannot block startup. Errors say what is wrong without quoting the content.
func ReadRegularFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fileError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("expected a readable regular file")
	}
	f, err := os.Open(path) //nolint:gosec // The deployment selects the file.
	if err != nil {
		return nil, fileError(err)
	}
	defer func() { _ = f.Close() }()
	return readBounded(f)
}

// fileError names why a file could not be read. Silo reads it as its own
// account, which in the omnibus image is not root, so a file only root can
// read is refused.
func fileError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return errors.New("the file does not exist")
	case errors.Is(err, fs.ErrPermission):
		return errors.New("permission denied: the account Silo runs as cannot read the file")
	default:
		return errors.New("expected a readable regular file")
	}
}
