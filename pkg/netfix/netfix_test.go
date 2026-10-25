package netfix

import (
	"errors"
	"reflect"
	"testing"
)

// fakeEnv builds Options backed by in-memory env vars and files.
func fakeEnv(goos string, env map[string]string, files map[string]string) Options {
	return Options{
		GOOS:   goos,
		Getenv: func(k string) string { return env[k] },
		ReadFile: func(path string) ([]byte, error) {
			if data, ok := files[path]; ok {
				return []byte(data), nil
			}
			return nil, errors.New("not found")
		},
	}
}

const termuxPrefix = "/data/data/com.termux/files/usr"

func TestParseResolvConf(t *testing.T) {
	data := []byte(`
# a comment
; another comment
nameserver 1.1.1.1
nameserver   8.8.8.8   # trailing comment
  nameserver	2001:4860:4860::8888
search example.com
options ndots:2
nameserver not-an-ip
nameserver
nameserver 9.9.9.9 extra-field
bogus line here
`)
	got := ParseResolvConf(data)
	want := []string{"1.1.1.1", "8.8.8.8", "2001:4860:4860::8888"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseResolvConf = %v, want %v", got, want)
	}
}

func TestIsLoopbackNameserver(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "127.0.1.5", "::1", "localhost", "LOCALHOST"} {
		if !IsLoopbackNameserver(addr) {
			t.Errorf("IsLoopbackNameserver(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"1.1.1.1", "8.8.8.8", "2001:4860:4860::8888", "192.168.1.1"} {
		if IsLoopbackNameserver(addr) {
			t.Errorf("IsLoopbackNameserver(%q) = true, want false", addr)
		}
	}
}

func TestNeedsFixAndroid(t *testing.T) {
	o := fakeEnv("android", nil, map[string]string{
		SystemResolvConfPath: "nameserver 8.8.8.8\n",
	})
	if !NeedsFix(o) {
		t.Fatal("android must always need the fix, even with a resolv.conf present")
	}
}

func TestNeedsFixTermuxEnvOnLinux(t *testing.T) {
	// The static linux/armv7 fallback binary running under Termux:
	// GOOS is linux, but the Termux markers are present.
	o := fakeEnv("linux", map[string]string{
		"TERMUX_VERSION": "0.118.0",
		"PREFIX":         termuxPrefix,
	}, map[string]string{
		SystemResolvConfPath: "nameserver 8.8.8.8\n",
	})
	if !NeedsFix(o) {
		t.Fatal("Termux markers must trigger the fix regardless of GOOS")
	}
}

func TestNeedsFixDesktopUntouched(t *testing.T) {
	o := fakeEnv("linux", nil, map[string]string{
		SystemResolvConfPath: "nameserver 192.168.1.1\nnameserver 1.1.1.1\n",
	})
	if NeedsFix(o) {
		t.Fatal("desktop linux with usable resolv.conf must not be touched")
	}
}

func TestNeedsFixMissingResolvConf(t *testing.T) {
	o := fakeEnv("linux", nil, nil)
	if !NeedsFix(o) {
		t.Fatal("missing /etc/resolv.conf (Go falls back to [::1]:53) must trigger the fix")
	}
}

func TestNeedsFixOnlyLoopbackResolvConf(t *testing.T) {
	// This is exactly the broken Android situation leaking into a
	// resolv.conf: only Go's default loopback servers.
	o := fakeEnv("linux", nil, map[string]string{
		SystemResolvConfPath: "nameserver 127.0.0.1\nnameserver ::1\n",
	})
	if !NeedsFix(o) {
		t.Fatal("loopback-only resolv.conf must trigger the fix")
	}
}

func TestNeedsFixWindowsUntouched(t *testing.T) {
	o := fakeEnv("windows", nil, nil)
	if NeedsFix(o) {
		t.Fatal("windows does not use resolv.conf; missing file must not trigger")
	}
}

func TestCollectNameserversPriorityAndFiltering(t *testing.T) {
	o := fakeEnv("android", map[string]string{
		"MAGIC_DNS": "9.9.9.9, 127.0.0.1 ,bogus",
		"PREFIX":    termuxPrefix,
	}, map[string]string{
		termuxPrefix + "/etc/resolv.conf": "nameserver 192.168.1.1\nnameserver ::1\n",
		SystemResolvConfPath:              "nameserver 203.0.113.7\nnameserver 192.168.1.1\n",
	})
	got := CollectNameservers(o)
	want := []string{
		"9.9.9.9",     // MAGIC_DNS override first; loopback + junk dropped
		"192.168.1.1", // $PREFIX resolv.conf; ::1 dropped
		"203.0.113.7", // system resolv.conf; duplicate 192.168.1.1 deduped
		"1.1.1.1",     // public fallbacks last
		"8.8.8.8",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CollectNameservers = %v, want %v", got, want)
	}
}

func TestCollectNameserversFallbackNeverEmpty(t *testing.T) {
	o := fakeEnv("android", nil, nil)
	got := CollectNameservers(o)
	if !reflect.DeepEqual(got, PublicFallback) {
		t.Fatalf("with no sources at all, CollectNameservers = %v, want public fallback %v", got, PublicFallback)
	}
}

func TestInstallSetsDefaultResolver(t *testing.T) {
	// Reset package state mutated by Install.
	installed = false
	installedServers = nil
	o := fakeEnv("android", map[string]string{"MAGIC_DNS": "9.9.9.9"}, nil)
	active, servers := Install(o)
	if !active {
		t.Fatal("Install on android must activate")
	}
	if len(servers) == 0 || servers[0] != "9.9.9.9" {
		t.Fatalf("Install servers = %v, want MAGIC_DNS first", servers)
	}
	// Second call is a no-op returning the same state.
	active2, servers2 := Install(o)
	if !active2 || !reflect.DeepEqual(servers, servers2) {
		t.Fatal("second Install must be a no-op")
	}
	installed = false
	installedServers = nil
}

func TestInstallSkipsHealthyDesktop(t *testing.T) {
	installed = false
	installedServers = nil
	o := fakeEnv("darwin", nil, map[string]string{
		SystemResolvConfPath: "nameserver 192.168.1.1\n",
	})
	active, _ := Install(o)
	if active {
		t.Fatal("Install must not activate on a healthy desktop")
	}
}
