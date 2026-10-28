package account

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type AdminRequest struct {
	Action        string           `json:"action"`
	Name          string           `json:"name,omitempty"`
	NewName       string           `json:"new_name,omitempty"`
	Quota         int64            `json:"quota_bytes,omitempty"`
	Users         []NewUser        `json:"users,omitempty"`
	Charges       map[string]int64 `json:"charges,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	AcceptGap     bool             `json:"accept_gap,omitempty"`
	ExpectedMonth string           `json:"expected_month,omitempty"`
}

type AdminResult struct {
	OK         bool    `json:"ok"`
	Error      string  `json:"error,omitempty"`
	Incomplete bool    `json:"incomplete,omitempty"`
	Status     *Status `json:"status,omitempty"`
	// This field is only transmitted over the authenticated local root socket.
	// The CLI writes it into private files; never print or log the raw response.
	Password string `json:"password,omitempty"`
}

func (d *Daemon) AdminHandler(w http.ResponseWriter, r *http.Request) {
	if r.Context().Value(peerKey{}) != true {
		http.Error(w, "root Unix peer required", 403)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/admin" || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if !slot(d.adminSlots) {
		http.Error(w, "busy", 503)
		return
	}
	defer closeSlot(d.adminSlots)
	var req AdminRequest
	if err := DecodeJSON(r.Body, &req, 65536); err != nil {
		jsonResponse(w, 400, AdminResult{Error: err.Error()})
		return
	}
	result := d.Admin(r.Context(), req, time.Now())
	code := 200
	if !result.OK {
		code = 409
	}
	jsonResponse(w, code, result)
}

func (d *Daemon) Admin(ctx context.Context, req AdminRequest, now time.Time) AdminResult {
	d.adminMu.Lock()
	defer d.adminMu.Unlock()
	fail := func(err error) AdminResult { return AdminResult{Error: err.Error()} }
	var err error
	switch req.Action {
	case "status", "list":
		v, err := d.Status(now)
		if err != nil {
			return fail(errors.New("account database unavailable"))
		}
		return AdminResult{OK: true, Status: &v}
	case "export":
		if err := requireName(req.Name); err != nil {
			return fail(err)
		}
		p, err := d.Store.Export(req.Name)
		if err != nil {
			return fail(err)
		}
		return AdminResult{OK: true, Password: p}
	case "add":
		err = d.Store.AddUsers([]NewUser{{Name: req.Name, Quota: req.Quota}}, now)
	case "import":
		if _, e := d.Collector.Generation(); e == nil {
			return fail(errors.New("imports require a stopped core and explicit identity mapping"))
		}
		err = d.Store.AddUsers(req.Users, now)
	case "arm":
		month, _ := billingTime(now)
		if req.ExpectedMonth != "" && req.ExpectedMonth != month {
			return fail(errors.New("billing month changed; review the gap and charges again"))
		}
		if _, e := d.Collector.Generation(); e == nil {
			return fail(errors.New("stop HY2 before approving a new core baseline"))
		}
		if _, e := ReadGuard(d.Config.GuardStatusFile, now); e != nil {
			return fail(e)
		}
		err = d.Store.Arm(req.Charges, req.Reason, req.AcceptGap, now)
	case "activate":
		if sharedBlocked(d.Config.HealthBlockedFile) {
			return fail(errors.New("shared-health block requires explicit root system arm"))
		}
		if _, e := ReadGuard(d.Config.GuardStatusFile, now); e != nil {
			return fail(e)
		}
		gen, e := d.Collector.Generation()
		if e != nil {
			return fail(errors.New("core is not live"))
		}
		m, e := d.Store.Meta()
		if e != nil || m.Generation != gen {
			return fail(errors.New("current core baseline not verified"))
		}
		err = d.Store.Activate(now)
	case "prepare-stop":
		// Hold new authentication first, then sample under the collector lock.
		// This is an explicit global maintenance command, never a user-ban path.
		if err = d.Store.BeginMaintenance(now); err == nil {
			err = d.Collector.CollectHeld(ctx, now)
			if e := d.Store.Hold("planned core maintenance; verify final sample/gap before restart", now); err == nil {
				err = e
			}
		}
	case "backup":
		err = d.Store.Backup(now)
	case "retry-ban":
		if err = requireName(req.Name); err == nil {
			err = d.Store.RetryBan(req.Name, now)
		}
	case "disable", "revoke", "quota", "enable", "rename", "rotate":
		if req.Action == "enable" {
			if sharedBlocked(d.Config.HealthBlockedFile) {
				return fail(errors.New("cannot enable through shared-health block"))
			}
			if _, e := ReadGuard(d.Config.GuardStatusFile, now); e != nil {
				return fail(e)
			}
		}
		err = d.Store.ChangeUser(req.Action, req.Name, req.NewName, req.Quota, now)
	default:
		return fail(errors.New("unknown admin command"))
	}
	if err != nil {
		return fail(err)
	}
	if req.Action == "disable" || req.Action == "revoke" || req.Action == "quota" || req.Action == "rotate" || req.Action == "retry-ban" {
		err = d.Collector.EnforceUser(ctx, now, req.Name)
		_ = d.publish(now)
		v, e := d.Store.View(now)
		if e != nil {
			return fail(errors.New("state persisted but ban status unavailable"))
		}
		for _, u := range v.Users {
			name := req.Name
			if req.Action == "rename" {
				name = req.NewName
			}
			if u.Name == name && (u.Pending > 0 || u.Failed > 0) {
				message := "new authentication denied where applicable; stored-session ban is incomplete; bounded retries only"
				if err != nil {
					message = err.Error()
				}
				return AdminResult{Incomplete: true, Error: message}
			}
		}
		if err != nil {
			return AdminResult{Incomplete: true, Error: err.Error()}
		}
	}
	// Updates to HTTP page cache cannot roll back a successful account mutation.
	_ = d.publish(now)
	return AdminResult{OK: true}
}

func (c *Collector) CollectHeld(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.pullLocked(ctx, now, true)
	if err != nil {
		c.lastFailure = err.Error()
	}
	return err
}

// The held sample requires a maintenance hold and preserves generation, review,
// delta validation and restore-witness checks. Its caller cannot authenticate.
func (s *Store) applyHeld(gen string, snapshot Snapshot, now time.Time) error {
	return s.apply(gen, snapshot, now, true)
}
