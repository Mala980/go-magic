// Package netfix installs a fallback DNS resolver for Android/Termux.
//
// Background: the Android binaries of go-magic are built either with
// CGO_ENABLED=0 (android/arm64) or as static Linux binaries
// (linux/armv7 fallback), so name resolution uses Go's pure resolver
// (netgo). That resolver reads /etc/resolv.conf — a file Android does
// not have. When it is missing, Go silently falls back to the default
// nameservers 127.0.0.1:53 and [::1]:53, where nothing listens on
// Android/Termux, and every lookup fails with:
//
//	lookup api.telegram.org on [::1]:53: read udp [::1]:...->[::1]:53: read: connection refused
//
// Termux keeps a working resolv.conf at $PREFIX/etc/resolv.conf
// (/data/data/com.termux/files/usr/etc/resolv.conf). netfix detects the
// Android/Termux situation, collects usable nameservers from Termux's
// resolv.conf (and an optional MAGIC_DNS override), and points
// net.DefaultResolver at them. Because the Telegram client, the LLM
// provider clients and every http.DefaultClient / net.Dialer with a nil
// Resolver all resolve through net.DefaultResolver, installing it once,
// early in main(), fixes DNS for the whole process.
//
// On a normal desktop (Linux/macOS/Windows with a usable system
// resolv.conf and no Android/Termux markers) netfix does nothing at
// all and the default resolver is left untouched.
package netfix

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Public fallback nameservers, used (in this order) after every
// locally discovered nameserver. They are only ever dialed when the
// earlier candidates fail to answer.
var PublicFallback = []string{"1.1.1.1", "8.8.8.8"}

// TermuxResolvConfPath is the well-known location of resolv.conf on a
// default Termux install, used when $PREFIX is not set in the
// environment (e.g. the binary was started by another Android app).
const TermuxResolvConfPath = "/data/data/com.termux/files/usr/etc/resolv.conf"

// SystemResolvConfPath is the standard Unix resolv.conf location.
const SystemResolvConfPath = "/etc/resolv.conf"

// Options carries the injectable environment netfix reads, so tests can
// exercise the detection logic without touching the real system.
type Options struct {
	// GOOS is the target OS to test for; empty means runtime.GOOS.
	GOOS string
	// Getenv reads an environment variable; nil means os.Getenv.
	Getenv func(string) string
	// ReadFile reads a file; nil means os.ReadFile.
	ReadFile func(string) ([]byte, error)
}

// withDefaults fills unset fields from the real process environment.
func (o Options) withDefaults() Options {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.ReadFile == nil {
		o.ReadFile = os.ReadFile
	}
	return o
}

// resolvConfPaths returns the resolv.conf locations to consult, in
// priority order: the Termux prefix first (when running under Termux
// the system path does not exist), then the well-known Termux path,
// then the system path.
func resolvConfPaths(o Options) []string {
	paths := []string{}
	if prefix := strings.TrimSpace(o.Getenv("PREFIX")); prefix != "" {
		paths = append(paths, filepath.Join(prefix, "etc", "resolv.conf"))
	}
	paths = append(paths, TermuxResolvConfPath, SystemResolvConfPath)
	// Dedupe while preserving order ($PREFIX usually equals the
	// well-known Termux prefix).
	seen := map[string]bool{}
	out := paths[:0]
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// ParseResolvConf extracts the nameserver addresses from resolv.conf
// contents. Comments (# and ;), blank lines, unknown directives and
// malformed entries are ignored.
func ParseResolvConf(data []byte) []string {
	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		// Strip comments: both '#' and ';' start a comment in
		// resolv.conf(5).
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "nameserver" {
			continue
		}
		if net.ParseIP(fields[1]) != nil {
			servers = append(servers, fields[1])
		}
	}
	return servers
}

// IsLoopbackNameserver reports whether addr is a loopback address
// (127.0.0.0/8, ::1) or the literal "localhost". These are exactly the
// broken defaults Go falls back to on Android, so they are never
// useful candidates for the fallback resolver.
func IsLoopbackNameserver(addr string) bool {
	if strings.EqualFold(addr, "localhost") {
		return true
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

// readNameservers returns the nameservers listed in the resolv.conf at
// path, or nil when the file cannot be read.
func readNameservers(o Options, path string) []string {
	data, err := o.ReadFile(path)
	if err != nil {
		return nil
	}
	return ParseResolvConf(data)
}

// systemNameservers returns the nameservers from the *system*
// resolv.conf only (not the Termux locations).
func systemNameservers(o Options) []string {
	return readNameservers(o, SystemResolvConfPath)
}

// HasUsableSystemResolver reports whether the system resolv.conf
// exists and lists at least one non-loopback nameserver, i.e. whether
// Go's default resolver configuration looks healthy.
func HasUsableSystemResolver(o Options) bool {
	o = o.withDefaults()
	for _, ns := range systemNameservers(o) {
		if !IsLoopbackNameserver(ns) {
			return true
		}
	}
	return false
}

// IsTermux reports whether the environment looks like Termux:
// TERMUX_VERSION is set, or PREFIX points inside the Termux app.
func IsTermux(o Options) bool {
	o = o.withDefaults()
	if strings.TrimSpace(o.Getenv("TERMUX_VERSION")) != "" {
		return true
	}
	return strings.Contains(o.Getenv("PREFIX"), "com.termux")
}

// NeedsFix reports whether the fallback resolver should be installed:
// running on Android, running under Termux (any GOOS — the Linux
// fallback binary also runs there), or the system resolver
// configuration is missing/unusable (no resolv.conf, or only loopback
// nameservers — Go's broken defaults).
func NeedsFix(o Options) bool {
	o = o.withDefaults()
	if o.GOOS == "android" || IsTermux(o) {
		return true
	}
	if o.GOOS == "windows" {
		// Windows name resolution does not use resolv.conf at all
		// (Go reads the OS DNS configuration), so a missing file
		// there is normal and must not trigger the fallback.
		return false
	}
	return !HasUsableSystemResolver(o)
}

// CollectNameservers builds the ordered, de-duplicated candidate list:
//
//  1. MAGIC_DNS environment override (comma-separated) — an explicit
//     user choice always wins;
//  2. nameservers from $PREFIX/etc/resolv.conf;
//  3. nameservers from the well-known Termux resolv.conf path;
//  4. nameservers from /etc/resolv.conf;
//  5. public fallbacks (1.1.1.1, 8.8.8.8).
//
// Loopback entries are dropped everywhere: on Android they are the
// bug, not a configuration. The public fallbacks guarantee the result
// is never empty.
func CollectNameservers(o Options) []string {
	o = o.withDefaults()
	var out []string
	seen := map[string]bool{}
	add := func(ns string) {
		ns = strings.TrimSpace(ns)
		if ns == "" || IsLoopbackNameserver(ns) {
			return
		}
		if net.ParseIP(ns) == nil {
			return
		}
		if !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	for _, ns := range strings.Split(o.Getenv("MAGIC_DNS"), ",") {
		add(ns)
	}
	for _, path := range resolvConfPaths(o) {
		for _, ns := range readNameservers(o, path) {
			add(ns)
		}
	}
	for _, ns := range PublicFallback {
		add(ns)
	}
	return out
}

// NewResolver builds a PreferGo resolver that dials the given
// nameservers in order. The network argument from the Go resolver is
// honored, so large/truncated answers still fall back from UDP to TCP
// exactly as with the standard resolver.
func NewResolver(servers []string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{}
			var lastErr error
			for _, srv := range servers {
				conn, err := d.DialContext(ctx, network, net.JoinHostPort(srv, "53"))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

// Install replaces net.DefaultResolver with the fallback resolver when
// NeedsFix says the environment needs it. It returns whether the
// fallback was installed and the nameserver list in use, so the
// caller can log one informative line. Safe to call more than once;
// later calls are no-ops once installed.
func Install(o Options) (bool, []string) {
	o = o.withDefaults()
	if installed {
		return true, installedServers
	}
	if !NeedsFix(o) {
		return false, nil
	}
	servers := CollectNameservers(o)
	net.DefaultResolver = NewResolver(servers)
	installed = true
	installedServers = servers
	return true, servers
}

var (
	installed        bool
	installedServers []string
)
