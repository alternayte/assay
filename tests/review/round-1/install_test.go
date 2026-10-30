package round1

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../../.."

// GitHub renames a release asset whose name starts with a period. install.sh
// downloads each file by its name from the release, so each file that it
// fetches must be a release asset whose name has no leading period.
func TestRVW_1_11_InstallFetchesDotfileReleaseAsset(t *testing.T) {
	gr, err := os.ReadFile(filepath.Join(root, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	uploaded := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*-\s*glob:\s*(\S+)`).FindAllStringSubmatch(string(gr), -1) {
		uploaded[filepath.Base(m[1])] = true
	}
	sh, err := os.ReadFile(filepath.Join(root, "scripts/install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fetched := regexp.MustCompile(`(?m)^fetch (\S+) `).FindAllStringSubmatch(string(sh), -1)
	if len(fetched) == 0 {
		t.Fatal("install.sh fetches no file")
	}
	for _, m := range fetched {
		name := m[1]
		if !uploaded[name] {
			t.Errorf("install.sh fetches %s, which the release does not upload", name)
		}
		if strings.HasPrefix(name, ".") {
			t.Errorf("install.sh fetches %s from the release; GitHub stores an asset with a leading period under another name", name)
		}
	}
}

// stubs puts fake docker and curl commands on PATH. docker compose fails
// when its -f file does not exist, as the real command does.
func stubs(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
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
	write("curl", "exit 0\n")
	return bin
}

func install(t *testing.T, bin, cwd string, args ...string) (string, error) {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{filepath.Join(abs, "scripts/install.sh")}, args...)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "ASSAY_INSTALL_SRC="+filepath.Join(abs, "deploy/compose"),
		"HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=", "ASSAY_IMAGE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// install.sh documents --dir as the install directory. A relative --dir
// must work, as it does for every other command line tool.
func TestRVW_1_12_InstallRelativeDirFails(t *testing.T) {
	bin, cwd := stubs(t), t.TempDir()
	if out, err := install(t, bin, cwd, "--dir", "assay-home"); err != nil {
		t.Errorf("install.sh --dir assay-home: %v\n%s", err, out)
	}
}

// .env holds the secret key, the database password and the setup token. A
// second run that changes .env must keep it readable by the owner only.
func TestRVW_1_13_InstallSecondRunMakesEnvWorldReadable(t *testing.T) {
	bin, dir := stubs(t), t.TempDir()
	if out, err := install(t, bin, dir, "--dir", dir); err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	if out, err := install(t, bin, dir, "--dir", dir, "--demo"); err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	fi, err := os.Stat(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf(".env has mode %v after a second run with --demo, want no group or other access", fi.Mode().Perm())
	}
}
