package account

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // Billing month must also work on a minimal VM without tzdata.
)

const GiB int64 = 1 << 30
const MaxBody = 4096
const MaxStatsBody = 4 << 20

var userName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var hostName = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
var billingLocation = mustBillingLocation()

func mustBillingLocation() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		panic("embedded billing timezone unavailable")
	}
	return loc
}

type Config struct {
	Database           string `json:"database"`
	Anchor             string `json:"anchor"`
	SecretsFile        string `json:"secrets_file"`
	AuthListen         string `json:"auth_listen"`
	PageListen         string `json:"page_listen"`
	AdminSocket        string `json:"admin_socket"`
	StatsURL           string `json:"stats_url"`
	GenerationFile     string `json:"generation_file"`
	GuardStatusFile    string `json:"guard_status_file"`
	HealthFile         string `json:"health_file"`
	HealthBlockedFile  string `json:"health_blocked_file"`
	Certificate        string `json:"certificate"`
	Server             string `json:"server"`
	SNI                string `json:"sni"`
	Port               int    `json:"port"`
	QuotaPoolBytes     int64  `json:"quota_pool_bytes"`
	MaxAccounts        int    `json:"max_accounts"`
	MaxSessionsPerUser int    `json:"max_sessions_per_user_generation"`
	MaxSessionsTotal   int    `json:"max_sessions_total_generation"`
	BackupDir          string `json:"backup_dir"`
}

type Secrets struct {
	StatsSecret   string `json:"stats_secret"`
	AuthKey       string `json:"auth_key"`
	CredentialKey string `json:"credential_key"`
	PageUser      string `json:"page_user"`
	PagePassword  string `json:"page_password"`
}

func DecodeJSON(r io.Reader, target any, limit int64) error {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("request exceeds size limit")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func LoadConfig(path string) (Config, Secrets, error) {
	var c Config
	var s Secrets
	f, err := os.Open(path)
	if err != nil {
		return c, s, err
	}
	defer f.Close()
	if err := DecodeJSON(f, &c, 16384); err != nil {
		return c, s, err
	}
	if err := c.Validate(); err != nil {
		return c, s, err
	}
	f2, err := os.Open(c.SecretsFile)
	if err != nil {
		return c, s, err
	}
	defer f2.Close()
	st, err := f2.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return c, s, errors.New("secrets file must be a private regular file (0600)")
	}
	if err := DecodeJSON(f2, &s, 8192); err != nil {
		return c, s, err
	}
	for _, key := range []string{s.AuthKey, s.CredentialKey} {
		b, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(b) != 32 {
			return c, s, errors.New("keys must be base64-encoded 32-byte keys")
		}
	}
	if len(s.StatsSecret) < 32 || len(s.PagePassword) < 32 || !userName.MatchString(s.PageUser) {
		return c, s, errors.New("independent strong stats/page credentials required")
	}
	keys := []string{s.AuthKey, s.CredentialKey, s.StatsSecret, s.PagePassword}
	for i, key := range keys {
		for _, other := range keys[i+1:] {
			if key == other {
				return c, s, errors.New("authentication, encryption, statistics and page keys must be independent")
			}
		}
	}
	return c, s, nil
}

func loopbackAddress(addr string) error {
	host, p, err := net.SplitHostPort(addr)
	if err != nil || p == "0" {
		return errors.New("explicit loopback host and nonzero port required")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("management interfaces must use literal loopback IPs")
	}
	if _, err := net.LookupPort("tcp", p); err != nil {
		return errors.New("invalid port")
	}
	return nil
}

func (c Config) Validate() error {
	paths := []string{c.Database, c.Anchor, c.SecretsFile, c.AdminSocket, c.GenerationFile, c.GuardStatusFile, c.HealthFile, c.HealthBlockedFile, c.Certificate, c.BackupDir}
	seen := map[string]bool{}
	for _, p := range paths {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || seen[p] {
			return errors.New("all file paths must be distinct, absolute and clean")
		}
		seen[p] = true
	}
	for _, dir := range []string{filepath.Dir(c.Database), c.BackupDir} {
		if c.Anchor == dir || strings.HasPrefix(c.Anchor, dir+string(os.PathSeparator)) {
			return errors.New("restore witness must be outside the database/backup directory tree")
		}
	}
	if err := loopbackAddress(c.AuthListen); err != nil {
		return err
	}
	if err := loopbackAddress(c.PageListen); err != nil {
		return err
	}
	u, err := url.Parse(c.StatsURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("stats_url must be a bare loopback HTTP origin")
	}
	if err := loopbackAddress(u.Host); err != nil {
		return err
	}
	if c.AuthListen == c.PageListen || c.AuthListen == u.Host || c.PageListen == u.Host {
		return errors.New("auth, page and statistics ports must differ")
	}
	if c.Port < 1 || c.Port > 65535 || c.Server == "" || c.SNI == "" || len(c.Server) > 253 || len(c.SNI) > 253 {
		return errors.New("server, SNI and port required")
	}
	if net.ParseIP(c.SNI) != nil || !hostName.MatchString(c.SNI) {
		return errors.New("SNI must be a concrete certificate hostname")
	}
	if net.ParseIP(c.Server) == nil && !hostName.MatchString(c.Server) {
		return errors.New("invalid server address")
	}
	if c.QuotaPoolBytes <= 0 || c.QuotaPoolBytes > 140*GiB {
		return errors.New("explicit quota pool must be in (0, 140 GiB]; reduce it for a partial month/shared budget")
	}
	if c.MaxAccounts < 1 || c.MaxAccounts > 32 || c.MaxSessionsPerUser < 8 || c.MaxSessionsPerUser > 2048 || c.MaxSessionsTotal < c.MaxSessionsPerUser || c.MaxSessionsTotal > 8192 {
		return errors.New("invalid account/session capacity bounds")
	}
	return nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func AtomicFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hy2-tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func writeJSON(path string, value any, mode os.FileMode) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return AtomicFile(path, append(b, '\n'), mode)
}

func billingTime(t time.Time) (month, day string) {
	return t.In(billingLocation).Format("2006-01"), t.In(billingLocation).Format("2006-01-02")
}

func requireName(name string) error {
	if !userName.MatchString(name) || name == "." || name == ".." {
		return fmt.Errorf("name must match [A-Za-z0-9._-]{1,64}")
	}
	return nil
}
