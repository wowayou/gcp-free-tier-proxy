package account

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const CoreVersion = "v2.13.0"
const CoreCommit = "ca8fbd874fd6ca82413948de6dc5924405e103e1"

func InitSecrets(path, pageCredentialPath string) error {
	for _, p := range []string{path, pageCredentialPath} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			return errors.New("refusing to overwrite existing credentials")
		}
	}
	key := func() (string, error) {
		b, err := randomToken(32)
		if err != nil {
			return "", err
		}
		raw, err := base64.RawURLEncoding.DecodeString(b)
		return base64.StdEncoding.EncodeToString(raw), err
	}
	auth, err := key()
	if err != nil {
		return err
	}
	enc, err := key()
	if err != nil {
		return err
	}
	stats, err := randomToken(32)
	if err != nil {
		return err
	}
	password, err := randomToken(32)
	if err != nil {
		return err
	}
	s := Secrets{StatsSecret: stats, AuthKey: auth, CredentialKey: enc, PageUser: "usage", PagePassword: password}
	if err := writeJSON(path, s, 0600); err != nil {
		return err
	}
	return AtomicFile(pageCredentialPath, []byte("username: usage\npassword: "+password+"\n"), 0600)
}

func ExportFiles(cfg Config, name, password, dir string) ([]string, error) {
	if err := requireName(name); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("export directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("export directory must be private (0700), not a symlink")
	}
	client := map[string]any{"type": "hysteria2", "tag": "hy2-" + name, "server": cfg.Server, "server_port": cfg.Port, "password": password, "network": "tcp", "tls": map[string]any{"enabled": true, "server_name": cfg.SNI, "insecure": false}}
	b, err := json.MarshalIndent(client, "", "  ")
	if err != nil {
		return nil, err
	}
	host := net.JoinHostPort(cfg.Server, fmt.Sprint(cfg.Port))
	link := "hysteria2://" + url.PathEscape(password) + "@" + host + "/?sni=" + url.QueryEscape(cfg.SNI) + "&insecure=0#" + url.PathEscape("hy2-"+name) + "\n"
	paths := []string{filepath.Join(dir, name+".json"), filepath.Join(dir, name+".txt")}
	if err := AtomicFile(paths[0], append(b, '\n'), 0600); err != nil {
		return nil, err
	}
	if err := AtomicFile(paths[1], []byte(link), 0600); err != nil {
		return nil, err
	}
	return paths, nil
}

// CoreConfig contains no shared user credential. Its only auth backend is the
// controller; UDP business forwarding is disabled independently of QUIC UDP.
func CoreConfig(cfg Config, secrets Secrets, listen, cert, key string) map[string]any {
	return map[string]any{
		"listen": listen, "tls": map[string]any{"cert": cert, "key": key, "sniGuard": "strict"},
		"auth":         map[string]any{"type": "http", "http": map[string]any{"url": "http://" + cfg.AuthListen + "/auth", "insecure": false}},
		"trafficStats": map[string]any{"listen": strings.TrimPrefix(cfg.StatsURL, "http://"), "secret": secrets.StatsSecret},
		"disableUDP":   true, "ignoreClientBandwidth": true, "speedTest": false,
		"congestion": map[string]any{"type": "bbr", "bbrProfile": "standard"},
		// Explicit system resolver: ACL IP rules must also see addresses for
		// domain targets, not only literal IPs. No GeoIP/GeoSite downloads.
		"resolver": map[string]any{"type": "system"},
		"acl":      map[string]any{"inline": PrivateACL()},
	}
}

func PrivateACL() []string {
	// Both literal and resolved destinations are matched by the official ACL.
	// No geo rules: no automatic GeoIP/GeoSite network downloads.
	//
	// Do NOT reject ::ffff:0:0/96 wholesale. The official engine represents an
	// IPv4 literal as a 16-byte IPv4-mapped address, so that CIDR would reject
	// every ordinary IPv4 destination. Reject only the mapped private/special
	// ranges instead; public IPv4 stays usable.
	return []string{
		"reject(suffix:metadata.google.internal)", "reject(suffix:metadata.goog)",
		"reject(0.0.0.0/8)", "reject(10.0.0.0/8)", "reject(100.64.0.0/10)", "reject(127.0.0.0/8)",
		"reject(169.254.0.0/16)", "reject(172.16.0.0/12)", "reject(192.168.0.0/16)",
		"reject(192.0.0.0/24)", "reject(198.18.0.0/15)", "reject(224.0.0.0/4)", "reject(240.0.0.0/4)",
		"reject(::ffff:0.0.0.0/104)", "reject(::ffff:10.0.0.0/104)", "reject(::ffff:100.64.0.0/106)",
		"reject(::ffff:127.0.0.0/104)", "reject(::ffff:169.254.0.0/112)", "reject(::ffff:172.16.0.0/108)",
		"reject(::ffff:192.0.0.0/120)", "reject(::ffff:192.168.0.0/112)", "reject(::ffff:198.18.0.0/111)",
		"reject(::ffff:224.0.0.0/100)", "reject(::ffff:240.0.0.0/100)",
		// ::/96 also covers the deprecated IPv4-compatible ::a.b.c.d forms.
		"reject(::/96)", "reject(::1/128)", "reject(64:ff9b::/96)",
		"reject(64:ff9b:1::/48)", "reject(100::/64)", "reject(2001::/32)", "reject(2002::/16)",
		"reject(fc00::/7)", "reject(fe80::/10)", "reject(ff00::/8)", "direct(all)",
	}
}

// Launcher records the actual child identity before it can return an auth
// response. The unmodified official binary does all proxying; controller has
// no service-management capability. systemd supervises this whole cgroup.
func LaunchCore(binary, config, generationFile string) error {
	version, err := exec.Command(binary, "version").Output()
	if err != nil || !strings.Contains(string(version), "Version:\t"+CoreVersion) || !strings.Contains(string(version), CoreCommit) || !strings.Contains(string(version), "BuildType:\trelease") {
		return errors.New("pinned official release core required")
	}
	cmd := exec.Command(binary, "--disable-update-check", "--log-level", "warn", "server", "-c", config)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := cmd.Start(); err != nil {
		return err
	}
	failed := true
	defer func() {
		if failed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	id, err := randomToken(24)
	if err != nil {
		return err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return err
	}
	ticks, err := ProcessTicks(cmd.Process.Pid)
	if err != nil {
		return err
	}
	g := ProcessGeneration{ID: id, BootID: strings.TrimSpace(string(boot)), PID: cmd.Process.Pid, StartTicks: ticks}
	if err := writeJSON(generationFile, g, 0640); err != nil {
		return err
	}
	// Avoid leaking a live child if invoked outside systemd and wrapper dies.
	// The unit also has KillMode=control-group and BindsTo=hy2-accountd.service.
	failed = false
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case sig := <-signals:
		_ = cmd.Process.Signal(sig)
		select {
		case err = <-done:
		case <-time.After(8 * time.Second):
			_ = cmd.Process.Kill()
			err = <-done
		}
	}
	_ = os.Remove(generationFile)
	return err
}

func ReadPrivateJSON(path string, target any, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return errors.New("input file must be private regular file (0600)")
	}
	return DecodeJSON(f, target, limit)
}

// Independent privileged health service: only shared faults reach this code.
// A failed personal /kick is NOT a shared fault and never invokes systemctl.
func HealthCheck(cfg Config, secrets Secrets, mode string) error {
	if mode != "gate" && mode != "run" {
		return errors.New("health mode must be gate or run")
	}
	if mode == "run" {
		control, err := LockHealthControl(cfg, 2*time.Second)
		if err != nil {
			if errors.Is(err, ErrHealthControlBusy) {
				// No stale decision was read. A bounded arm/shutdown owns the
				// lock; the unchanged timer will check again on its next tick.
				return err
			}
			// Unwritable storage must not disable protection. Arm also cannot
			// acquire this lock, so refuse recovery and still attempt shutdown.
			return fmt.Errorf("health control lock unavailable: %w; %v", err, stopForSharedHealth(cfg))
		}
		defer control.Close()
	}
	// Read only after acquiring the control lock: an arm may have published
	// a new bootstrap while a previous health invocation was waiting.
	now := time.Now()
	key, err := base64.StdEncoding.DecodeString(secrets.AuthKey)
	if err != nil {
		return err
	}
	h, healthErr := VerifyHealth(cfg.HealthFile, key, now)
	_, guardErr := ReadGuard(cfg.GuardStatusFile, now)
	if mode == "gate" {
		if guardErr != nil {
			return guardErr
		}
		if healthErr != nil {
			return healthErr
		}
		if _, err := os.Lstat(cfg.HealthBlockedFile); !os.IsNotExist(err) {
			return errors.New("persistent shared-health block requires explicit system arm")
		}
		if !h.Bootstrap {
			return errors.New("core startup requires explicitly armed maintenance baseline")
		}
		return nil
	}
	if healthErr == nil && guardErr == nil && (!sharedBlocked(cfg.HealthBlockedFile) || h.Bootstrap) {
		return nil
	}
	return stopForSharedHealth(cfg)
}

func stopForSharedHealth(cfg Config) error {
	// Separate durable latch belongs to root, outside accountd write access.
	var stopErr error
	if err := AtomicFile(cfg.HealthBlockedFile, []byte("shared health failure; verify accounting before restart\n"), 0600); err != nil {
		// Still stop on a full/unwritable disk; persistence failure is not an
		// excuse to leave unmetered existing sessions forwarding.
		stopErr = err
	}
	if err := boundedSystemctl("stop", "hysteria-server.service"); err != nil {
		stopErr = err
	}
	// The early generator overrides /etc units and owns exactly this latch.
	// No static mask is created/removed that could belong to an operator.
	if err := boundedSystemctl("daemon-reload"); err != nil {
		stopErr = err
	}
	for prop, expected := range map[string]string{"ActiveState": "inactive", "LoadState": "masked"} {
		out, err := boundedSystemctlOutput("show", "hysteria-server.service", "--property="+prop, "--value")
		state := string(bytes.TrimSpace(out))
		if err != nil || (prop == "ActiveState" && state != "inactive" && state != "failed") || (prop == "LoadState" && state != expected) {
			stopErr = errors.New("HY2 shutdown/mask verification failed")
		}
	}
	if _, err := ReadGeneration(cfg.GenerationFile); err == nil {
		stopErr = errors.New("HY2 core is still live after shared-health shutdown")
	}
	if stopErr != nil {
		return errors.New("HY2 health shutdown incomplete; manual intervention required")
	}
	return errors.New("HY2 held stopped for shared accounting/guard fault")
}

func boundedSystemctl(args ...string) error {
	_, err := boundedSystemctlOutput(args...)
	return err
}

func boundedSystemctlOutput(args ...string) ([]byte, error) {
	cmd := exec.Command("systemctl", args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Do not expose the buffer until Wait returns (the pipe copier may still
	// be writing). Timeout kills this bounded client's process group only.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return output.Bytes(), err
	case <-time.After(8 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return nil, errors.New("systemctl timeout")
	}
}
