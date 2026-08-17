package viamntp

import (
	"context"
	"net"
	"testing"
	"time"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	genericservice "go.viam.com/rdk/services/generic"
	"go.viam.com/test"
)

// fakeNTPServer speaks just enough SNTP to answer a client, reporting a clock
// that is skewed from the local one by the given amount.
func fakeNTPServer(t *testing.T, skew time.Duration) string {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	test.That(t, err, test.ShouldBeNil)
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 128)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < ntpPacketSize {
				continue
			}
			resp := make([]byte, ntpPacketSize)
			resp[0] = byte(ntpVersion<<3 | ntpModeServer)
			resp[1] = 2 // stratum
			copy(resp[24:32], buf[40:48])
			now := time.Now().Add(skew)
			putNTPTime(resp[32:40], now)
			putNTPTime(resp[40:48], now)
			if _, err := conn.WriteTo(resp, addr); err != nil {
				return
			}
		}
	}()

	return conn.LocalAddr().String()
}

// newTestService builds the service pointed at the given server rather than
// the hardcoded pool, with a background loop too slow to fire during the test
// unless a case asks for it.
func newTestService(t *testing.T, cfg *Config, server string) *viamNtpUpdate {
	t.Helper()
	return newTestServiceEvery(t, cfg, server, time.Hour)
}

func newTestServiceEvery(t *testing.T, cfg *Config, server string, interval time.Duration) *viamNtpUpdate {
	t.Helper()
	svc, err := newService(genericservice.Named("ntp"), cfg, logging.NewTestLogger(t), server, interval)
	test.That(t, err, test.ShouldBeNil)
	t.Cleanup(func() { test.That(t, svc.Close(context.Background()), test.ShouldBeNil) })
	return svc
}

func TestNTPTimestampRoundTrip(t *testing.T) {
	buf := make([]byte, 8)
	want := time.Date(2026, 8, 17, 12, 34, 56, 123456789, time.UTC)

	putNTPTime(buf, want)
	got := getNTPTime(buf)

	// The 32-bit fraction resolves to ~233ps, and encode and decode each
	// truncate, so a couple of nanoseconds is the honest tolerance.
	test.That(t, abs(got.Sub(want)) <= 2*time.Nanosecond, test.ShouldBeTrue)
}

func TestGetNTPTimeZeroIsZeroTime(t *testing.T) {
	test.That(t, getNTPTime(make([]byte, 8)).IsZero(), test.ShouldBeTrue)
}

func TestQueryMeasuresOffset(t *testing.T) {
	addr := fakeNTPServer(t, 90*time.Second)

	res := queryNTP(addr, 2*time.Second)
	test.That(t, res.Err, test.ShouldBeNil)
	test.That(t, res.Stratum, test.ShouldEqual, uint8(2))
	// Allow generous slack for scheduling on a loaded machine.
	test.That(t, abs(res.Offset-90*time.Second) < time.Second, test.ShouldBeTrue)
}

func TestQueryUnreachableServer(t *testing.T) {
	res := queryNTP("127.0.0.1:1", 500*time.Millisecond)
	test.That(t, res.Err, test.ShouldNotBeNil)
}

func TestDoCommandQuery(t *testing.T) {
	addr := fakeNTPServer(t, 45*time.Second)
	svc := newTestService(t, &Config{}, addr)

	resp, err := svc.DoCommand(context.Background(), map[string]interface{}{"query": map[string]interface{}{}})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, resp["server"], test.ShouldEqual, addr)
	test.That(t, resp["offset_ms"].(float64), test.ShouldBeGreaterThan, 44000.0)
	test.That(t, resp["offset_ms"].(float64), test.ShouldBeLessThan, 46000.0)
}

// A dry run must report the offset it would have applied without writing.
func TestDoCommandUpdateDryRun(t *testing.T) {
	addr := fakeNTPServer(t, 120*time.Second)
	svc := newTestService(t, &Config{}, addr)

	resp, err := svc.DoCommand(context.Background(), map[string]interface{}{
		"update": map[string]interface{}{"dry_run": true},
	})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, resp["applied"], test.ShouldBeFalse)
	test.That(t, resp["reason"], test.ShouldEqual, "dry_run")
	test.That(t, resp["offset_ms"].(float64), test.ShouldBeGreaterThan, 119000.0)
}

// An offset under the threshold is not worth a clock write.
func TestUpdateSuppressedBelowThreshold(t *testing.T) {
	addr := fakeNTPServer(t, 0)
	svc := newTestService(t, &Config{}, addr)

	resp, err := svc.DoCommand(context.Background(), map[string]interface{}{"update": map[string]interface{}{}})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, resp["applied"], test.ShouldBeFalse)
	test.That(t, resp["reason"], test.ShouldContainSubstring, "threshold")

	// ...and the skip is recorded in status.
	resp, err = svc.DoCommand(context.Background(), map[string]interface{}{"status": map[string]interface{}{}})
	test.That(t, err, test.ShouldBeNil)
	last := resp["last_sync"].(map[string]interface{})
	test.That(t, last["applied"], test.ShouldBeFalse)
	test.That(t, last["reason"], test.ShouldContainSubstring, "threshold")
}

func TestUpdateServerUnreachable(t *testing.T) {
	svc := newTestService(t, &Config{}, "127.0.0.1:1")

	_, err := svc.DoCommand(context.Background(), map[string]interface{}{"update": map[string]interface{}{}})
	test.That(t, err, test.ShouldNotBeNil)
	test.That(t, err.Error(), test.ShouldContainSubstring, "failed after 3 attempts")

	resp, err := svc.DoCommand(context.Background(), map[string]interface{}{"status": map[string]interface{}{}})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, resp["last_sync"].(map[string]interface{})["error"], test.ShouldNotBeNil)
}

func TestDoCommandSetTimeDryRun(t *testing.T) {
	svc := newTestService(t, &Config{}, "127.0.0.1:1")

	resp, err := svc.DoCommand(context.Background(), map[string]interface{}{
		"set_time": map[string]interface{}{"time": "2030-01-01T00:00:00Z", "dry_run": true},
	})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, resp["applied"], test.ShouldBeFalse)
	test.That(t, resp["target_time"], test.ShouldEqual, "2030-01-01T00:00:00Z")
}

func TestDoCommandSetTimeRejectsBadInput(t *testing.T) {
	svc := newTestService(t, &Config{}, "127.0.0.1:1")

	for _, args := range []map[string]interface{}{
		{"dry_run": true},
		{"time": "not a time", "dry_run": true},
		{"time": 12345.0, "dry_run": true},
		{"unix": "nope", "dry_run": true},
	} {
		_, err := svc.DoCommand(context.Background(), map[string]interface{}{"set_time": args})
		test.That(t, err, test.ShouldNotBeNil)
	}
}

// waitForSync blocks until the service has recorded an attempt.
func waitForSync(t *testing.T, svc *viamNtpUpdate) *syncRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		last := svc.lastSync
		svc.mu.Unlock()
		if last != nil {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no sync was recorded before the deadline")
	return nil
}

// The background loop must run on its own, with no command and no
// sync_on_start. Skew is zero so the tick lands below the threshold and never
// tries to write the clock.
func TestBackgroundLoopSyncs(t *testing.T) {
	addr := fakeNTPServer(t, 0)
	svc := newTestServiceEvery(t, &Config{}, addr, 20*time.Millisecond)

	last := waitForSync(t, svc)
	test.That(t, last.Applied, test.ShouldBeFalse)
	test.That(t, last.Err, test.ShouldBeEmpty)
	test.That(t, last.Reason, test.ShouldContainSubstring, "threshold")
}

func TestSyncOnStart(t *testing.T) {
	addr := fakeNTPServer(t, 0)
	svc := newTestServiceEvery(t, &Config{SyncOnStart: true}, addr, time.Hour)

	// Only the startup sync can have produced this; the loop will not tick
	// for an hour.
	last := waitForSync(t, svc)
	test.That(t, last.Err, test.ShouldBeEmpty)
}

// Close must stop the loop and return promptly.
func TestCloseStopsLoop(t *testing.T) {
	addr := fakeNTPServer(t, 0)
	svc, err := newService(genericservice.Named("ntp"), &Config{SyncOnStart: true},
		logging.NewTestLogger(t), addr, 10*time.Millisecond)
	test.That(t, err, test.ShouldBeNil)

	done := make(chan error, 1)
	go func() { done <- svc.Close(context.Background()) }()

	select {
	case err := <-done:
		test.That(t, err, test.ShouldBeNil)
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return")
	}
}

func TestDoCommandStatus(t *testing.T) {
	svc := newTestService(t, &Config{SyncOnStart: false}, defaultServer)

	resp, err := svc.DoCommand(context.Background(), map[string]interface{}{"status": map[string]interface{}{}})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, resp["last_sync"], test.ShouldBeNil)
	test.That(t, resp["server"], test.ShouldEqual, defaultServer)
	test.That(t, resp["sync_on_start"], test.ShouldBeFalse)
	test.That(t, resp["can_set_clock"], test.ShouldEqual, canSetClock())
}

// The embedded resource.Named must be backed by a real value. Left nil, as the
// scaffold leaves it, any method this type does not implement itself is a
// segfault rather than a compile error.
func TestEmbeddedNamedIsInitialized(t *testing.T) {
	svc := newTestService(t, &Config{}, defaultServer)

	test.That(t, svc.Named, test.ShouldNotBeNil)
	// Reached through the embedding, not the explicit method.
	test.That(t, svc.Named.Name(), test.ShouldResemble, genericservice.Named("ntp"))

	st, err := svc.Named.Status(context.Background())
	test.That(t, err, test.ShouldBeNil)
	test.That(t, st, test.ShouldBeEmpty)
}

// Status is required by resource.Resource and is reached directly by the RDK,
// not only through DoCommand. The explicit method must win over the embedded
// fallback, which returns an empty map.
func TestStatusIsImplemented(t *testing.T) {
	svc := newTestService(t, &Config{}, defaultServer)

	st, err := svc.Status(context.Background())
	test.That(t, err, test.ShouldBeNil)
	test.That(t, st["server"], test.ShouldEqual, defaultServer)

	// The status command must go through the same implementation.
	viaCmd, err := svc.DoCommand(context.Background(), map[string]interface{}{"status": map[string]interface{}{}})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, viaCmd["server"], test.ShouldEqual, st["server"])
	test.That(t, viaCmd["sync_interval"], test.ShouldEqual, st["sync_interval"])
}

func TestDoCommandUnknown(t *testing.T) {
	svc := newTestService(t, &Config{}, "127.0.0.1:1")

	_, err := svc.DoCommand(context.Background(), map[string]interface{}{"nope": map[string]interface{}{}})
	test.That(t, err, test.ShouldNotBeNil)

	_, err = svc.DoCommand(context.Background(), map[string]interface{}{})
	test.That(t, err, test.ShouldNotBeNil)
}

func TestParseCommandForms(t *testing.T) {
	// {"update": {"force": true}}
	name, args, err := parseCommand(map[string]interface{}{"update": map[string]interface{}{"force": true}})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, name, test.ShouldEqual, "update")
	test.That(t, args["force"], test.ShouldBeTrue)

	// {"command": "update", "force": true}
	name, args, err = parseCommand(map[string]interface{}{"command": "Update", "force": true})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, name, test.ShouldEqual, "update")
	test.That(t, args["force"], test.ShouldBeTrue)

	// bare {"status": true}
	name, _, err = parseCommand(map[string]interface{}{"status": true})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, name, test.ShouldEqual, "status")

	_, _, err = parseCommand(map[string]interface{}{"command": 7})
	test.That(t, err, test.ShouldNotBeNil)
}

// The public constructor must produce a service wired to the fixed defaults.
// No network happens here: sync_on_start is off and the loop will not tick.
func TestNewUpdateDefaults(t *testing.T) {
	res, err := NewUpdate(context.Background(), resource.Dependencies{},
		genericservice.Named("ntp"), &Config{}, logging.NewTestLogger(t))
	test.That(t, err, test.ShouldBeNil)
	defer func() { test.That(t, res.Close(context.Background()), test.ShouldBeNil) }()

	svc := res.(*viamNtpUpdate)
	test.That(t, svc.server, test.ShouldEqual, defaultServer)
	test.That(t, svc.interval, test.ShouldEqual, time.Hour)
}

func TestConfigValidate(t *testing.T) {
	req, opt, err := (&Config{SyncOnStart: true}).Validate("services.0")
	test.That(t, err, test.ShouldBeNil)
	test.That(t, req, test.ShouldBeEmpty)
	test.That(t, opt, test.ShouldBeEmpty)
}
