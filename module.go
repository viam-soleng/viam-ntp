// Package viamntp implements a generic service that reads the time from
// pool.ntp.org and steps the host's system clock to match.
package viamntp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	genericservice "go.viam.com/rdk/services/generic"
)

// Update is the viam:viam-ntp:update model of the generic service API.
var Update = resource.NewModel("viam", "viam-ntp", "update")

const (
	defaultServer = "pool.ntp.org"
	queryTimeout  = 5 * time.Second
	// A lost UDP packet is the common failure, so ask again before giving up.
	queryAttempts = 3
	// Below this, stepping the clock is not worth the syscall.
	minOffset = 100 * time.Millisecond
	// How often the background loop re-checks the clock.
	syncInterval = time.Hour
)

func init() {
	resource.RegisterService(genericservice.API, Update,
		resource.Registration[resource.Resource, *Config]{
			Constructor: newViamNtpUpdate,
		},
	)
}

// Config is the JSON configuration for the service.
type Config struct {
	// SyncOnStart runs one update as soon as the service starts.
	SyncOnStart bool `json:"sync_on_start,omitempty"`
}

// Validate ensures all parts of the config are valid and important fields exist.
// Returns three values:
//  1. Required dependencies: other resources that must exist for this resource to work.
//  2. Optional dependencies: other resources that may exist but are not required.
//  3. An error if any Config fields are missing or invalid.
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	return nil, nil, nil
}

// syncRecord is the outcome of the most recent update attempt.
type syncRecord struct {
	At      time.Time
	Offset  time.Duration
	Applied bool
	Reason  string
	Err     string
}

var _ resource.Resource = (*viamNtpUpdate)(nil)

type viamNtpUpdate struct {
	resource.AlwaysRebuild
	resource.Named

	name   resource.Name
	logger logging.Logger
	cfg    *Config

	server   string
	interval time.Duration

	cancelCtx  context.Context
	cancelFunc func()
	workers    sync.WaitGroup

	mu       sync.Mutex
	lastSync *syncRecord
}

func newViamNtpUpdate(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}

	return NewUpdate(ctx, deps, rawConf.ResourceName(), conf, logger)
}

// NewUpdate builds the service directly, for use from the CLI harness.
func NewUpdate(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *Config, logger logging.Logger) (resource.Resource, error) {
	return newService(name, conf, logger, defaultServer, syncInterval)
}

// newService is the real constructor. Tests call it with a local fake server
// and a short interval; everything else gets the fixed production values.
func newService(name resource.Name, conf *Config, logger logging.Logger,
	server string, interval time.Duration,
) (*viamNtpUpdate, error) {
	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &viamNtpUpdate{
		Named:      name.AsNamed(),
		name:       name,
		logger:     logger,
		cfg:        conf,
		server:     server,
		interval:   interval,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}

	if !canSetClock() {
		logger.Warn("Insufficient privileges to set the system clock; run viam-server as root (Administrator on Windows)")
	}

	if conf.SyncOnStart {
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			if _, err := s.sync(false, false); err != nil {
				logger.Errorw("Startup time sync failed", "error", err)
			}
		}()
	}

	s.workers.Add(1)
	go s.syncLoop()

	return s, nil
}

// syncLoop re-checks the clock on a fixed interval until the service closes.
func (s *viamNtpUpdate) syncLoop() {
	defer s.workers.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Infow("Starting background time sync", "interval", s.interval.String(), "server", s.server)
	for {
		select {
		case <-s.cancelCtx.Done():
			return
		case <-ticker.C:
			if _, err := s.sync(false, false); err != nil {
				s.logger.Errorw("Scheduled time sync failed", "error", err)
			}
		}
	}
}

func (s *viamNtpUpdate) Name() resource.Name {
	return s.name
}

// query asks the NTP server, retrying a few times before giving up.
func (s *viamNtpUpdate) query() (*ntpResult, error) {
	var lastErr error
	for range queryAttempts {
		res := queryNTP(s.server, queryTimeout)
		if res.Err == nil {
			return res, nil
		}
		lastErr = res.Err
	}
	return nil, fmt.Errorf("querying %s failed after %d attempts: %w", s.server, queryAttempts, lastErr)
}

// sync queries the server and steps the system clock, recording the outcome.
func (s *viamNtpUpdate) sync(force, dryRun bool) (map[string]interface{}, error) {
	res, err := s.query()
	if err != nil {
		s.record(&syncRecord{At: time.Now().UTC(), Err: err.Error()})
		return nil, err
	}

	offset := res.Offset
	out := resultMap(res)
	rec := &syncRecord{At: time.Now().UTC(), Offset: offset}

	skip := func(reason string) (map[string]interface{}, error) {
		out["applied"] = false
		out["reason"] = reason
		out["system_time"] = time.Now().UTC().Format(time.RFC3339Nano)
		rec.Reason = reason
		s.record(rec)
		return out, nil
	}

	switch {
	case dryRun:
		s.logger.Infow("Dry run: not changing the clock", "offset", offset.String())
		return skip("dry_run")
	case !force && abs(offset) < minOffset:
		return skip(fmt.Sprintf("offset %s is below the %s threshold", offset, minOffset))
	}

	target := time.Now().Add(offset)
	if err := setSystemTime(target); err != nil {
		err = fmt.Errorf("Failed to set the system clock (needs root/CAP_SYS_TIME): %w", err)
		rec.Err = err.Error()
		s.record(rec)
		return nil, err
	}

	s.logger.Infow("Stepped the system clock", "offset", offset.String(), "server", res.Server,
		"new_time", target.UTC().Format(time.RFC3339Nano))

	rec.Applied = true
	s.record(rec)
	out["applied"] = true
	out["system_time"] = time.Now().UTC().Format(time.RFC3339Nano)
	return out, nil
}

func (s *viamNtpUpdate) record(rec *syncRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSync = rec
}

// DoCommand is the whole surface of this service. Commands may be given either
// as {"update": {...}} or as {"command": "update", ...}.
func (s *viamNtpUpdate) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	name, args, err := parseCommand(cmd)
	if err != nil {
		return nil, err
	}

	switch name {
	case "query":
		return s.doQuery()
	case "update", "sync":
		return s.doUpdate(args)
	case "set_time":
		return s.doSetTime(args)
	case "status":
		return s.Status(ctx)
	default:
		return nil, fmt.Errorf("unknown command %q; supported commands are %s", name, strings.Join(commandNames, ", "))
	}
}

var commandNames = []string{"query", "update", "sync", "set_time", "status"}

// doQuery reports what the server says without touching the clock.
func (s *viamNtpUpdate) doQuery() (map[string]interface{}, error) {
	res, err := s.query()
	if err != nil {
		return nil, err
	}
	out := resultMap(res)
	out["system_time"] = time.Now().UTC().Format(time.RFC3339Nano)
	return out, nil
}

// doUpdate queries NTP and steps the clock.
func (s *viamNtpUpdate) doUpdate(args map[string]interface{}) (map[string]interface{}, error) {
	force, err := argBool(args, "force", false)
	if err != nil {
		return nil, err
	}
	dryRun, err := argBool(args, "dry_run", false)
	if err != nil {
		return nil, err
	}
	return s.sync(force, dryRun)
}

// doSetTime steps the clock to an explicitly supplied time, for machines with
// no route to an NTP server.
func (s *viamNtpUpdate) doSetTime(args map[string]interface{}) (map[string]interface{}, error) {
	var target time.Time

	switch {
	case args["time"] != nil:
		raw, ok := args["time"].(string)
		if !ok {
			return nil, fmt.Errorf("set_time: \"time\" must be an RFC3339 string")
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("set_time: could not parse %q as RFC3339: %w", raw, err)
		}
		target = parsed
	case args["unix"] != nil:
		secs, err := argFloat(args, "unix")
		if err != nil {
			return nil, err
		}
		target = time.Unix(0, int64(secs*float64(time.Second))).UTC()
	default:
		return nil, fmt.Errorf("set_time: supply either \"time\" (RFC3339) or \"unix\" (epoch seconds)")
	}

	dryRun, err := argBool(args, "dry_run", false)
	if err != nil {
		return nil, err
	}
	offset := time.Until(target)

	out := map[string]interface{}{
		"target_time": target.UTC().Format(time.RFC3339Nano),
		"offset_ms":   durationMs(offset),
		"offset":      offset.String(),
	}
	if dryRun {
		out["applied"] = false
		out["reason"] = "dry_run"
		out["system_time"] = time.Now().UTC().Format(time.RFC3339Nano)
		return out, nil
	}

	if err := setSystemTime(target); err != nil {
		return nil, fmt.Errorf("failed to set the system clock (needs root/CAP_SYS_TIME): %w", err)
	}
	s.logger.Infow("Set the system clock explicitly", "new_time", target.UTC().Format(time.RFC3339Nano))
	s.record(&syncRecord{At: time.Now().UTC(), Offset: offset, Applied: true})

	out["applied"] = true
	out["system_time"] = time.Now().UTC().Format(time.RFC3339Nano)
	return out, nil
}

// Status reports the local clock and the last update attempt. It is required
// by resource.Resource: the embedded resource.Named is an interface, and this
// type leaves it nil, so a missing implementation here is a nil dereference
// rather than a compile error.
func (s *viamNtpUpdate) Status(ctx context.Context) (map[string]interface{}, error) {
	now := time.Now()
	zone, offsetSec := now.Zone()

	out := map[string]interface{}{
		"system_time":         now.UTC().Format(time.RFC3339Nano),
		"local_time":          now.Format(time.RFC3339Nano),
		"timezone":            zone,
		"timezone_offset_sec": float64(offsetSec),
		"server":              s.server,
		"sync_interval":       s.interval.String(),
		"sync_on_start":       s.cfg.SyncOnStart,
		"can_set_clock":       canSetClock(),
	}

	s.mu.Lock()
	last := s.lastSync
	s.mu.Unlock()

	if last == nil {
		out["last_sync"] = nil
		return out, nil
	}
	sync := map[string]interface{}{
		"at":        last.At.Format(time.RFC3339Nano),
		"applied":   last.Applied,
		"offset_ms": durationMs(last.Offset),
		"offset":    last.Offset.String(),
	}
	if last.Reason != "" {
		sync["reason"] = last.Reason
	}
	if last.Err != "" {
		sync["error"] = last.Err
	}
	out["last_sync"] = sync
	return out, nil
}

func (s *viamNtpUpdate) Close(context.Context) error {
	s.cancelFunc()
	s.workers.Wait()
	return nil
}

func resultMap(res *ntpResult) map[string]interface{} {
	return map[string]interface{}{
		"server":        res.Server,
		"ntp_time":      res.NTPTime.Format(time.RFC3339Nano),
		"offset_ms":     durationMs(res.Offset),
		"offset":        res.Offset.String(),
		"round_trip_ms": durationMs(res.RoundTrip),
		"stratum":       float64(res.Stratum),
	}
}

// parseCommand accepts both {"update": {"force": true}} and
// {"command": "update", "force": true}.
func parseCommand(cmd map[string]interface{}) (string, map[string]interface{}, error) {
	if raw, ok := cmd["command"]; ok {
		name, ok := raw.(string)
		if !ok {
			return "", nil, fmt.Errorf("\"command\" must be a string")
		}
		return strings.ToLower(strings.TrimSpace(name)), cmd, nil
	}
	for _, name := range commandNames {
		raw, ok := cmd[name]
		if !ok {
			continue
		}
		if args, ok := raw.(map[string]interface{}); ok {
			return name, args, nil
		}
		return name, map[string]interface{}{}, nil
	}
	return "", nil, fmt.Errorf("No command given; supported commands are %s", strings.Join(commandNames, ", "))
}

func argBool(args map[string]interface{}, key string, fallback bool) (bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback, nil
	}
	v, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%q must be a boolean", key)
	}
	return v, nil
}

func argFloat(args map[string]interface{}, key string) (float64, error) {
	switch v := args[key].(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("%q must be a number", key)
	}
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
