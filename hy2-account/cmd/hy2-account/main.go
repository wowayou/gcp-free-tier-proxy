package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"example.com/gcp-free-tier-proxy/hy2-account/internal/account"
)

func main() {
	// Private database/WAL/socket/credential artifacts even when invoked by hand.
	syscall.Umask(0077)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
func run() error {
	args := os.Args[1:]
	if len(args) == 0 {
		return usage()
	}
	mode := args[0]
	args = args[1:]
	if filepath.Base(os.Args[0]) == "hy2-admin" {
		mode = "admin"
		args = os.Args[1:]
	}
	global := flag.NewFlagSet(mode, flag.ContinueOnError)
	cfgPath := global.String("config", "/etc/hy2-account/config.json", "configuration file")
	if err := global.Parse(args); err != nil {
		return err
	}
	args = global.Args()
	if mode == "version" {
		fmt.Println("hy2-account 1; core", account.CoreVersion, account.CoreCommit)
		return nil
	}
	if mode == "secrets-init" {
		if os.Geteuid() != 0 {
			return errors.New("root required")
		}
		if len(args) != 2 {
			return errors.New("secrets-init requires private secrets path and private page-credential path")
		}
		return account.InitSecrets(args[0], args[1])
	}
	if mode == "core-run" {
		if len(args) != 3 {
			return errors.New("core-run requires binary, config, generation file")
		}
		return account.LaunchCore(args[0], args[1], args[2])
	}
	cfg, secrets, err := account.LoadConfig(*cfgPath)
	if err != nil {
		return err
	}
	switch mode {
	case "init":
		if os.Geteuid() != 0 {
			return errors.New("root required")
		}
		s, err := account.OpenStore(cfg, secrets, true, time.Now())
		if err != nil {
			return err
		}
		return s.Close()
	case "serve":
		s, err := account.OpenStore(cfg, secrets, false, time.Now())
		if err != nil {
			return err
		}
		defer s.Close()
		c := &account.Collector{Store: s, API: account.NewHTTPAPI(cfg.StatsURL, secrets.StatsSecret), Generation: func() (string, error) { return account.ReadGeneration(cfg.GenerationFile) }, Clock: time.Now}
		d := account.NewDaemon(s, c, cfg, secrets)
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		return d.Run(ctx)
	case "admin":
		if os.Geteuid() != 0 {
			return errors.New("root-only administration")
		}
		return admin(cfg, args)
	case "health":
		if os.Geteuid() != 0 {
			return errors.New("root required")
		}
		if len(args) != 1 {
			return errors.New("health gate|run required")
		}
		return account.HealthCheck(cfg, secrets, args[0])
	case "render-core":
		if os.Geteuid() != 0 {
			return errors.New("root required")
		}
		if len(args) != 4 {
			return errors.New("render-core requires listen, cert, key, output")
		}
		b, err := json.MarshalIndent(account.CoreConfig(cfg, secrets, args[0], args[1], args[2]), "", "  ")
		if err != nil {
			return err
		}
		return account.AtomicFile(args[3], append(b, '\n'), 0600)
	default:
		return usage()
	}
}
func usage() error {
	return errors.New("usage: hy2-account serve|init|version|secrets-init|core-run|render-core|health|admin; hy2-admin [--config PATH] user add|list|quota|disable|enable|revoke|rotate|rename|export|retry-ban; status; system arm|activate|prepare-stop|backup")
}

func quotaBytes(value string) (int64, error) {
	// Exact rational parsing prevents float rounding, NaN/Inf and scientific
	// notation from silently changing a byte quota.
	if len(value) > 16 {
		return 0, errors.New("invalid quota GiB")
	}
	for _, r := range value {
		if (r < '0' || r > '9') && r != '.' {
			return 0, errors.New("quota must be decimal GiB")
		}
	}
	r, ok := new(big.Rat).SetString(value)
	if !ok || r.Sign() <= 0 || r.Cmp(big.NewRat(140, 1)) > 0 {
		return 0, errors.New("quota GiB must be in (0, 140]")
	}
	r.Mul(r, big.NewRat(account.GiB, 1))
	n := new(big.Int).Quo(r.Num(), r.Denom()).Int64()
	if n <= 0 {
		return 0, errors.New("quota too small")
	}
	return n, nil
}

func call(cfg account.Config, req account.AdminRequest) (account.AdminResult, error) {
	var result account.AdminResult
	b, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", cfg.AdminSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	r, err := client.Post("http://local/admin", "application/json", bytes.NewReader(b))
	if err != nil {
		return result, errors.New("accountd socket unavailable")
	}
	defer r.Body.Close()
	if err := account.DecodeJSON(r.Body, &result, 1<<20); err != nil {
		return result, err
	}
	if !result.OK {
		return result, errors.New(result.Error)
	}
	return result, nil
}

func admin(cfg account.Config, args []string) error {
	if len(args) == 0 {
		return usage()
	}
	var req account.AdminRequest
	dir := "/root/hy2-credentials"
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return usage()
		}
		req.Action = "status"
	case "user":
		if len(args) < 2 {
			return usage()
		}
		req.Action = args[1]
		if req.Action == "list" {
			if len(args) != 2 {
				return usage()
			}
			break
		}
		if len(args) < 3 {
			return usage()
		}
		req.Name = args[2]
		flags := flag.NewFlagSet("user "+req.Action, flag.ContinueOnError)
		gib := flags.String("quota-gib", "", "quota GiB for add")
		quota := flags.String("gib", "", "quota GiB for quota")
		out := flags.String("output-dir", dir, "private credential output directory")
		if req.Action == "rename" {
			if len(args) != 4 {
				return usage()
			}
			req.NewName = args[3]
		} else {
			if err := flags.Parse(args[3:]); err != nil {
				return err
			}
			if len(flags.Args()) != 0 {
				return usage()
			}
		}
		dir = *out
		if req.Action == "add" {
			n, err := quotaBytes(*gib)
			if err != nil {
				return err
			}
			req.Quota = n
		}
		if req.Action == "quota" {
			n, err := quotaBytes(*quota)
			if err != nil {
				return err
			}
			req.Quota = n
		}
	case "system":
		if len(args) < 2 {
			return usage()
		}
		req.Action = args[1]
		if req.Action == "arm" {
			flags := flag.NewFlagSet("system arm", flag.ContinueOnError)
			charges := flags.String("charges-file", "", "root-only per-account conservative byte charges JSON")
			reason := flags.String("reason", "", "reason for verification")
			accept := flags.Bool("accept-gap", false, "acknowledge initial/missing measurements")
			if err := flags.Parse(args[2:]); err != nil {
				return err
			}
			if len(flags.Args()) != 0 || *charges == "" {
				return usage()
			}
			if err := account.ReadPrivateJSON(*charges, &req.Charges, 8192); err != nil {
				return err
			}
			req.Reason = *reason
			req.AcceptGap = *accept
			// Repair is an explicitly authorized global maintenance action. Never
			// grant user enable/quota an unmask/resume path.
			if err := verifyCoreStopped(); err != nil {
				return err
			}
		} else if len(args) != 2 {
			return usage()
		}
	case "import":
		if len(args) != 2 {
			return errors.New("import requires root-only account-mapping JSON file")
		}
		req.Action = "import"
		if err := verifyCoreStopped(); err != nil {
			return err
		}
		// Documented, self-describing mapping: {"users":[{name,password,quota_bytes}]}.
		var mapping struct {
			Users []account.NewUser `json:"users"`
		}
		if err := account.ReadPrivateJSON(args[1], &mapping, 65536); err != nil {
			return err
		}
		if len(mapping.Users) == 0 {
			return errors.New("import mapping must contain a non-empty \"users\" list")
		}
		req.Users = mapping.Users
	default:
		return usage()
	}
	result, err := call(cfg, req)
	if err != nil {
		return err
	}
	if req.Action == "arm" {
		if err := os.Remove(cfg.HealthBlockedFile); err != nil && !os.IsNotExist(err) {
			return errors.New("baseline armed but shared-health latch could not be cleared")
		}
		// Only our generator-owned latch is cleared. Operator/legacy masks and
		// the independent global egress block are never removed here.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
		cancel()
		if err != nil {
			return errors.New("baseline armed but daemon-reload failed")
		}
	}
	if result.Status != nil {
		if req.Action == "list" {
			for _, u := range result.Status.Users {
				fmt.Printf("%s\t%s\tused=%d quota=%d B\tban_pending=%d failed=%d\n", u.Name, u.Status, u.Used, u.Quota, u.Pending, u.Failed)
			}
			return nil
		}
		b, err := json.MarshalIndent(result.Status, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(b))
		return err
	}
	if req.Action == "export" || req.Action == "add" || req.Action == "rotate" {
		if req.Action != "export" {
			result, err = call(cfg, account.AdminRequest{Action: "export", Name: req.Name})
			if err != nil {
				return err
			}
		}
		paths, err := account.ExportFiles(cfg, req.Name, result.Password, dir)
		if err != nil {
			return err
		}
		for _, p := range paths {
			fmt.Println("Private credential file:", p)
		}
	} else if req.Action == "revoke" {
		for _, ext := range []string{".json", ".txt"} {
			if err := os.Remove(filepath.Join(dir, req.Name+ext)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	fmt.Println("Completed:", req.Action, "(no proxy restart)")
	return nil
}
func verifyCoreStopped() error {
	// CLI-side check complements the daemon's live-generation check. A masked
	// inactive unit is acceptable; activating/deactivating/unknown is not.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "show", "hysteria-server.service", "--property=ActiveState", "--value")
	out, err := cmd.Output()
	if err != nil {
		return errors.New("cannot verify hysteria-server stopped")
	}
	if string(bytes.TrimSpace(out)) != "inactive" && string(bytes.TrimSpace(out)) != "failed" {
		return errors.New("stop hysteria-server in the authorized maintenance window first")
	}
	return nil
}
