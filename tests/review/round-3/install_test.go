package round3

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../../.."

// stubs puts fake docker and curl commands on PATH. docker compose fails
// when its -f file does not exist, as the real command does. curl writes
// each call to curl.log with the proxy variables that it sees, and writes
// a file for -o.
func stubs(t *testing.T) (bin, log string) {
	t.Helper()
	bin = t.TempDir()
	log = filepath.Join(bin, "curl.log")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("docker", `
if [ "$1" = compose ]; then
	shift
	while [ $# -gt 0 ]; do
		case "$1" in
		-f) [ -f "$2" ] || { echo "no file $2" >&2; exit 1; }; shift 2 ;;
		*) shift ;;
		esac
	done
fi
exit 0
`)
	write("curl", `
printf '%s|HTTP_PROXY=%s|http_proxy=%s\n' "$*" "${HTTP_PROXY:-}" "${http_proxy:-}" >>"`+log+`"
while [ $# -gt 0 ]; do
	case "$1" in
	-o) printf '#!/bin/sh\n# Install and start Assay with Docker Compose.\n' >"$2"; shift 2 ;;
	*) shift ;;
	esac
done
exit 0
`)
	return bin, log
}

// install runs scripts/install.sh, or stdin when script is empty, with a
// clean proxy environment and the extra variables.
func install(t *testing.T, bin, cwd, home string, extra []string, stdin []byte, args ...string) (string, error) {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	argv := append([]string{filepath.Join(abs, "scripts/install.sh")}, args...)
	if stdin != nil {
		argv = append([]string{"-s", "--"}, args...)
	}
	cmd := exec.Command("sh", argv...)
	cmd.Dir = cwd
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":/usr/bin:/bin", "ASSAY_INSTALL_SRC="+filepath.Join(abs, "deploy/compose"),
		"HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=", "http_proxy=", "https_proxy=", "no_proxy=", "ASSAY_IMAGE=")
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func envValue(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	v := ""
	for _, l := range strings.Split(string(b), "\n") {
		if k, val, ok := strings.Cut(l, "="); ok && k == name {
			v = val
		}
	}
	return v
}

// install.sh tells the user to stop Assay with the copy in the install
// directory, but it never writes that copy. A pinned --version stays at
// the first value. The readyz poll of localhost goes through the proxy,
// and the lowercase proxy variables of most shells are ignored.
func TestRVW_3_12_InstallScriptProblems(t *testing.T) {
	t.Run("saves itself and stops from the install directory", func(t *testing.T) {
		bin, _ := stubs(t)
		home := t.TempDir()
		if out, err := install(t, bin, home, home, nil, nil); err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		dir := filepath.Join(home, ".assay")
		cmd := exec.Command("sh", "install.sh", "--down")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bin+":/usr/bin:/bin")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("sh install.sh --down in %s: %v\n%s", dir, err, out)
		}
	})

	t.Run("saves itself from a pipe", func(t *testing.T) {
		bin, _ := stubs(t)
		home := t.TempDir()
		script, err := os.ReadFile(filepath.Join(root, "scripts/install.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if out, err := install(t, bin, home, home, nil, script); err != nil {
			t.Fatalf("install from a pipe: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(home, ".assay", "install.sh")); err != nil {
			t.Errorf("install from a pipe writes no install.sh: %v", err)
		}
	})

	t.Run("a new version updates a pinned version", func(t *testing.T) {
		bin, _ := stubs(t)
		home := t.TempDir()
		dir := filepath.Join(home, ".assay")
		if out, err := install(t, bin, home, home, nil, nil, "--version", "v1.0.0"); err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if out, err := install(t, bin, home, home, nil, nil, "--version", "v1.1.0"); err != nil {
			t.Fatalf("second install: %v\n%s", err, out)
		}
		if v := envValue(t, dir, "ASSAY_VERSION"); v != "v1.1.0" {
			t.Errorf("ASSAY_VERSION is %q after install --version v1.1.0", v)
		}
	})

	t.Run("readyz does not use the proxy and lowercase proxy variables count", func(t *testing.T) {
		bin, log := stubs(t)
		home := t.TempDir()
		dir := filepath.Join(home, ".assay")
		proxy := []string{"http_proxy=http://proxy.acme.test:3128", "https_proxy=http://proxy.acme.test:3128", "no_proxy=intranet.acme.test"}
		if out, err := install(t, bin, home, home, proxy, nil); err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if v := envValue(t, dir, "HTTPS_PROXY"); v != "http://proxy.acme.test:3128" {
			t.Errorf("HTTPS_PROXY in .env is %q with https_proxy set", v)
		}
		if v := envValue(t, dir, "NO_PROXY"); !strings.Contains(v, "intranet.acme.test") || !strings.Contains(v, "db") {
			t.Errorf("NO_PROXY in .env is %q with no_proxy set", v)
		}
		b, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if strings.Contains(l, "/readyz") && !strings.Contains(l, "--noproxy") {
				t.Errorf("the readyz poll can use the proxy: curl %s", l)
				break
			}
		}
	})
}
