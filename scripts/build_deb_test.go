package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildDebPackagesSystemdCommandSocketConfig(t *testing.T) {
	repoRoot := testRepoRoot(t)
	requireCommand(t, "dpkg-deb")

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "go-monitoring")
	writeExecutable(t, fakeBinary, `#!/bin/sh
set -eu

if [ "${1:-}" != "config" ]; then
	echo "unexpected command: ${1:-}" >&2
	exit 2
fi
shift

config_file=""
metrics_listener=""
control_listener=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		--config)
			config_file="$2"
			shift 2
			;;
		--listener)
			case "$2" in
				name=metrics,*) metrics_listener="$2" ;;
				name=control,*) control_listener="$2" ;;
			esac
			shift 2
			;;
		*)
			shift
			;;
	esac
done

if [ "$metrics_listener" != "name=metrics,address=127.0.0.1:45876,apis=metrics" ]; then
	echo "unexpected metrics listener: $metrics_listener" >&2
	exit 3
fi
if [ "$control_listener" != "name=control,address=unix:/run/go-monitoring/agent.sock,apis=commands" ]; then
	echo "unexpected control listener: $control_listener" >&2
	exit 4
fi

mkdir -p "$(dirname "$config_file")"
cat > "$config_file" <<'JSON'
{
  "version": 1,
  "listeners": [
    {"name": "metrics", "address": "127.0.0.1:45876", "apis": ["metrics"]},
    {"name": "control", "address": "unix:/run/go-monitoring/agent.sock", "apis": ["commands"]}
  ],
  "collector_interval": "15s",
  "history": "cpu,mem,diskio,network,containers",
  "cache_ttl": {}
}
JSON

echo "Saved config: $config_file"
`)

	outDir := filepath.Join(tmpDir, "dist")
	cmd := exec.Command("bash", "scripts/build-deb.sh", fakeBinary, outDir, "v1.4.0-test")
	cmd.Dir = repoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build-deb.sh failed: %v\n%s", err, output)
	}

	deb := filepath.Join(outDir, "go-monitoring_1.4.0~test_amd64.deb")
	extractDir := filepath.Join(tmpDir, "extract")
	cmd = exec.Command("dpkg-deb", "-x", deb, extractDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("extract deb failed: %v\n%s", err, output)
	}

	configBytes, err := os.ReadFile(filepath.Join(extractDir, "etc/go-monitoring/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configBytes), "Saved config:") {
		t.Fatalf("packaged config included CLI status output:\n%s", configBytes)
	}

	var cfg struct {
		Listeners []struct {
			Name    string   `json:"name"`
			Address string   `json:"address"`
			APIs    []string `json:"apis"`
		} `json:"listeners"`
	}
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		t.Fatalf("packaged config is not valid JSON: %v\n%s", err, configBytes)
	}

	var foundControl bool
	for _, listener := range cfg.Listeners {
		if listener.Name == "control" {
			foundControl = true
			if listener.Address != "unix:/run/go-monitoring/agent.sock" {
				t.Fatalf("control listener address = %q", listener.Address)
			}
			if len(listener.APIs) != 1 || listener.APIs[0] != "commands" {
				t.Fatalf("control listener APIs = %#v", listener.APIs)
			}
		}
	}
	if !foundControl {
		t.Fatalf("packaged config is missing control listener: %#v", cfg.Listeners)
	}
}

func TestInstallScriptAvoidsInteractiveConffilePrompt(t *testing.T) {
	repoRoot := testRepoRoot(t)
	contents, err := os.ReadFile(filepath.Join(repoRoot, "packaging/install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	for _, want := range []string{
		"DEBIAN_FRONTEND=noninteractive",
		"--force-confdef",
		"--force-confold",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("install.sh missing %q", want)
		}
	}
}

func TestPostinstMigratesUserRuntimeCommandSocket(t *testing.T) {
	repoRoot := testRepoRoot(t)
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
  "version": 1,
  "listeners": [
    {"name": "control", "address": "unix:/run/user/1001/go-monitoring/agent.sock", "apis": ["commands"]}
  ]
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "systemctl"), "#!/bin/sh\nexit 0\n")

	cmd := exec.Command("sh", filepath.Join(repoRoot, "packaging/deb/postinst"), "configure")
	cmd.Env = append(os.Environ(),
		"GO_MONITORING_CONFIG_FILE="+configPath,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("postinst failed: %v\n%s", err, output)
	}

	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), "unix:/run/user/1001/go-monitoring/agent.sock") {
		t.Fatalf("postinst did not migrate user runtime socket:\n%s", updated)
	}
	if !strings.Contains(string(updated), "unix:/run/go-monitoring/agent.sock") {
		t.Fatalf("postinst missing systemd runtime socket:\n%s", updated)
	}
	if _, err := os.Stat(configPath + ".bak"); err != nil {
		t.Fatalf("postinst did not create backup: %v", err)
	}
}

func TestInstalledSmokeScriptIsNonDestructive(t *testing.T) {
	repoRoot := testRepoRoot(t)
	scriptPath := filepath.Join(repoRoot, "scripts/smoke-installed.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("smoke-installed.sh is not executable: mode %v", info.Mode())
	}

	contents, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)

	for _, want := range []string{
		"systemctl is-active",
		"http://localhost/healthz",
		"commands.list",
		"config.get",
		"status.get",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("smoke-installed.sh missing %q", want)
		}
	}

	for _, forbidden := range []string{
		"apt-get install",
		"apt install",
		"dpkg -i",
		"systemctl restart",
		"systemctl reload",
		"systemctl stop",
		"systemctl start",
		"systemctl enable",
		"systemctl disable",
		" rm ",
		" sed -i",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("smoke-installed.sh contains destructive operation %q", forbidden)
		}
	}
}

func TestMakeTestCanRunInstalledSmokeAfterDeadcode(t *testing.T) {
	repoRoot := testRepoRoot(t)
	contents, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(contents)

	if !strings.Contains(makefile, "RUN_INSTALLED_SMOKE ?= 0") {
		t.Fatal("Makefile does not define RUN_INSTALLED_SMOKE default")
	}
	if !strings.Contains(makefile, `if [ "$(RUN_INSTALLED_SMOKE)" = "1" ]`) {
		t.Fatal("Makefile does not gate installed smoke behind RUN_INSTALLED_SMOKE=1")
	}

	deadcodeIndex := strings.Index(makefile, "$(MAKE) --no-print-directory deadcode-only")
	smokeIndex := strings.Index(makefile, "$(MAKE) --no-print-directory smoke-installed")
	if deadcodeIndex < 0 || smokeIndex < 0 {
		t.Fatalf("Makefile missing deadcode or smoke invocation")
	}
	if smokeIndex < deadcodeIndex {
		t.Fatal("installed smoke should run after deadcode")
	}
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}

func requireCommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not available: %v", name, err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}
