// Package nodeinstall is the "silo node" command, which installs a Mac's
// transcode node as a LaunchAgent.
package nodeinstall

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/joho/godotenv"

	"github.com/Silo-Server/silo-server/internal/buildinfo"
	"github.com/Silo-Server/silo-server/internal/envutil"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

const nodeCommandUsage = `usage:
  silo node install [--server URL] [--new-key] [--port N]
      Install this Mac's transcode node as a LaunchAgent, or upgrade it to this binary
  silo node uninstall
      Stop the node and remove it, keeping its log
  silo node version
      Print this build's revision and build number`

const nodeInstallUsage = `usage: silo node install [--server URL] [--new-key] [--port N]

Installs this Mac's transcode node as a LaunchAgent for the current user, or
upgrades it to this binary. A rerun keeps every value it is not given, and any
other variable added to node.env, so "silo node install" alone upgrades in
place.

The node key Admin > Nodes issued is asked for at a hidden prompt the first
time and with --new-key. To script an install, pipe the key in on standard
input instead. It is never taken from the command line, where other users of
this Mac could read it.

  --server URL   the Silo server's URL as this Mac reaches it, such as
                 http://localhost:8090 (required the first time)
  --new-key      replace the installed node key
  --port N       the port the node listens on (default 8100)`

// nodeLaunchdLabel names the node's LaunchAgent.
const nodeLaunchdLabel = "org.siloserver.node"

// defaultNodeInstallPort is the installed node's port. A node otherwise
// listens on 8080, which Docker Desktop often publishes already.
const defaultNodeInstallPort = 8100

// nodeKeyPrompt marks a run that asks for a new key (--new-key).
const nodeKeyPrompt = "-"

// nodeExitTimeout is how long launchd should wait after SIGTERM before it kills
// a node that drains for drain: the drain and a minute to shut down. Current
// macOS caps ExitTimeOut at 60 seconds, so the installer sends SIGTERM itself
// and waits for the drain before its bootout.
func nodeExitTimeout(drain time.Duration) time.Duration {
	return addCapped(drain, time.Minute)
}

func addCapped(d, extra time.Duration) time.Duration {
	if d > math.MaxInt64-extra {
		return math.MaxInt64
	}
	return d + extra
}

// nodeEnvDrainTimeout is the drain the node takes from node.env's other
// variables. A value the node would not accept is an error.
func nodeEnvDrainTimeout(extra map[string]string) (time.Duration, error) {
	drain, err := transcodenode.ParseDrainTimeout(extra[transcodenode.EnvDrainTimeout])
	if err != nil {
		return 0, fmt.Errorf("node.env: %w", err)
	}
	return drain, nil
}

// Run serves "silo node install|uninstall|version".
func Run(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(nodeCommandUsage)
	}
	switch args[0] {
	case "install":
		return installNode(args[1:], in, out)
	case "uninstall":
		if len(args) != 1 {
			return errors.New(nodeCommandUsage)
		}
		return uninstallNode(out)
	case "version":
		if len(args) != 1 {
			return errors.New(nodeCommandUsage)
		}
		_, err := fmt.Fprintln(out, nodeVersion(buildinfo.Current()))
		return err
	case "-h", "-help", "--help", "help":
		_, err := fmt.Fprintln(out, nodeCommandUsage)
		return err
	default:
		return errors.New(nodeCommandUsage)
	}
}

// nodeVersion names a build as Admin shows the server's and each node's: the
// build number and the display revision, or the revision alone.
func nodeVersion(info buildinfo.Info) string {
	if info.BuildNumber == 0 {
		return info.Display
	}
	return strconv.FormatUint(info.BuildNumber, 10) + " · " + info.Display
}

// nodeInstallSettings is what an install sets. A zero field is one not given
// or not stored.
type nodeInstallSettings struct {
	server string
	key    string
	port   int
}

// parseNodeInstallFlags reads install's flags. help reports -h or --help.
func parseNodeInstallFlags(args []string) (given nodeInstallSettings, help bool, err error) {
	flags := flag.NewFlagSet("silo node install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&given.server, "server", "", "")
	newKey := flags.Bool("new-key", false, "")
	flags.IntVar(&given.port, "port", 0, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nodeInstallSettings{}, true, nil
		}
		return nodeInstallSettings{}, false, fmt.Errorf("%w\n%s", err, nodeInstallUsage)
	}
	if flags.NArg() != 0 {
		return nodeInstallSettings{}, false, errors.New(nodeInstallUsage)
	}
	given.server = strings.TrimSpace(given.server)
	if *newKey {
		given.key = nodeKeyPrompt
	}
	// A flag given is checked by its parsed value, so --port 00 is no port
	// and --server "" no server.
	flags.Visit(func(f *flag.Flag) {
		switch {
		case err != nil:
		case f.Name == "port" && (given.port < 1 || given.port > 65535):
			err = errors.New("--port must be between 1 and 65535")
		case f.Name == "server" && given.server == "":
			err = errors.New("--server needs a value")
		}
	})
	return given, false, err
}

// mergeNodeInstall lays the flags given over the existing install's values
// and fills in the default port.
func mergeNodeInstall(stored, given nodeInstallSettings) nodeInstallSettings {
	merged := stored
	if given.server != "" {
		merged.server = given.server
	}
	if given.key != "" {
		merged.key = given.key
	}
	if given.port != 0 {
		merged.port = given.port
	}
	if merged.port == 0 {
		merged.port = defaultNodeInstallPort
	}
	return merged
}

// nodeEnvManaged are the node.env variables the installer writes. A node key
// set directly would clash with the key file, so it is dropped as well.
var nodeEnvManaged = []string{"MODE", transcodenode.EnvCoreURL, transcodenode.EnvNodeKey + envutil.SecretFileSuffix, "PORT", transcodenode.EnvNodeKey}

// storedNodeInstall reads an existing install's settings from its node.env
// and node-key contents. extra holds the other variables in node.env, which
// a rerun keeps.
func storedNodeInstall(env, key []byte) (stored nodeInstallSettings, extra map[string]string, err error) {
	values, err := godotenv.Unmarshal(string(env))
	if err != nil {
		return nodeInstallSettings{}, nil, fmt.Errorf("node.env: %w", err)
	}
	stored = nodeInstallSettings{server: strings.TrimSpace(values[transcodenode.EnvCoreURL]), key: strings.TrimSpace(string(key))}
	if port, err := strconv.Atoi(strings.TrimSpace(values["PORT"])); err == nil {
		stored.port = port
	}
	for _, name := range nodeEnvManaged {
		delete(values, name)
	}
	return stored, values, nil
}

// nodeInstallPaths are an install's files for the user whose home is home.
type nodeInstallPaths struct {
	dir    string
	binary string
	env    string
	key    string
	lock   string
	log    string
	plist  string
}

func nodeInstallPathsIn(home string) nodeInstallPaths {
	dir := filepath.Join(home, "Library", "Application Support", "Silo", "node")
	return nodeInstallPaths{
		dir:    dir,
		binary: filepath.Join(dir, "silo"),
		env:    filepath.Join(dir, "node.env"),
		key:    filepath.Join(dir, "node-key"),
		lock:   filepath.Join(filepath.Dir(dir), "node-install.lock"), // outside dir, which uninstall removes under the lock
		log:    filepath.Join(home, "Library", "Logs", "Silo", "node.log"),
		plist:  filepath.Join(home, "Library", "LaunchAgents", nodeLaunchdLabel+".plist"),
	}
}

// renderNodeEnv writes node.env: the mode, the server's URL, the key file and
// the port, then the other variables an existing install carried, in name
// order. The node takes everything else from the server.
func renderNodeEnv(s nodeInstallSettings, keyFile string, extra map[string]string) ([]byte, error) {
	entries := [][2]string{
		{"MODE", transcodenode.Mode},
		{transcodenode.EnvCoreURL, s.server},
		{transcodenode.EnvNodeKey + envutil.SecretFileSuffix, keyFile},
		{"PORT", strconv.Itoa(s.port)},
	}
	for _, name := range slices.Sorted(maps.Keys(extra)) {
		entries = append(entries, [2]string{name, extra[name]})
	}
	var b strings.Builder
	for _, entry := range entries {
		value, err := quoteNodeEnv(entry[1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry[0], err)
		}
		fmt.Fprintf(&b, "%s=%s\n", entry[0], value)
	}
	return []byte(b.String()), nil
}

// quoteNodeEnv quotes value for godotenv, which the node reads node.env with:
// in double quotes, with $ escaped so it is not expanded, or in single quotes,
// which expand nothing, when it holds a double quote. godotenv cannot read
// back every backslash, nor a value with both quotes, so those are refused,
// with control characters.
func quoteNodeEnv(value string) (string, error) {
	if strings.Contains(value, `\`) || strings.ContainsFunc(value, unicode.IsControl) ||
		(strings.Contains(value, `"`) && strings.Contains(value, "'")) {
		return "", errors.New("the value holds a backslash, a control character, or both kinds of quote, which node.env cannot carry")
	}
	if strings.Contains(value, `"`) {
		return "'" + value + "'", nil
	}
	return `"` + strings.ReplaceAll(value, "$", `\$`) + `"`, nil
}

// renderNodePlist writes the LaunchAgent that runs the installed node, which
// drains for drain on SIGTERM.
func renderNodePlist(p nodeInstallPaths, drain time.Duration) []byte {
	var b bytes.Buffer
	str := func(s string) {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(s))
		b.WriteString("</string>\n")
	}
	key := func(k string) { b.WriteString("\t<key>" + k + "</key>\n\t") }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key("Label")
	str(nodeLaunchdLabel)
	key("ProgramArguments")
	b.WriteString("<array>\n")
	for _, arg := range []string{p.binary, "-env", p.env} {
		b.WriteString("\t\t")
		str(arg)
	}
	b.WriteString("\t</array>\n")
	key("WorkingDirectory")
	str(p.dir)
	key("StandardOutPath")
	str(p.log)
	key("StandardErrorPath")
	str(p.log)
	key("RunAtLoad")
	b.WriteString("<true/>\n")
	// Restarted after a crash or a failed exit, but not after a drained node's
	// clean exit, which the installer's stop waits for.
	key("KeepAlive")
	b.WriteString("<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	key("ProcessType")
	str("Interactive")
	key("ExitTimeOut")
	fmt.Fprintf(&b, "<integer>%d</integer>\n", int(nodeExitTimeout(drain)/time.Second))
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}
