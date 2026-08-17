package viamntp

import (
	"fmt"
	"net"
	"time"
)

// A minimal SNTP (RFC 5905 §5) client. Only the client half of the protocol is
// needed here, so pulling in a full NTP library would cost more in dependency
// surface than it saves in code.
const (
	// Seconds between the NTP epoch (1900-01-01) and the Unix epoch (1970-01-01).
	ntpEpochOffset = 2208988800

	ntpPort       = "123"
	ntpPacketSize = 48
	ntpVersion    = 4
	ntpModeClient = 3
	ntpModeServer = 4
	leapNotInSync = 3
	maxStratum    = 15
)

// ntpResult is the server's answer to a single query.
type ntpResult struct {
	Server    string
	NTPTime   time.Time     // the server's idea of "now", at the moment we asked
	Offset    time.Duration // how far the local clock is behind the server (positive => local is slow)
	RoundTrip time.Duration
	Stratum   uint8
	Err       error
}

func durationMs(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// queryNTP performs one SNTP exchange. The offset it returns is measured
// against the local clock at the time of the call.
func queryNTP(server string, timeout time.Duration) *ntpResult {
	res := &ntpResult{Server: server}

	addr := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		addr = net.JoinHostPort(server, ntpPort)
	}

	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		res.Err = fmt.Errorf("dial %s: %w", addr, err)
		return res
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		res.Err = fmt.Errorf("set deadline: %w", err)
		return res
	}

	req := make([]byte, ntpPacketSize)
	req[0] = byte(ntpVersion<<3 | ntpModeClient)

	// t1: our transmit time. The server echoes it back in the origin field,
	// which lets us reject replies that do not belong to this request.
	t1 := time.Now()
	putNTPTime(req[40:48], t1)

	if _, err := conn.Write(req); err != nil {
		res.Err = fmt.Errorf("write: %w", err)
		return res
	}

	resp := make([]byte, ntpPacketSize)
	n, err := conn.Read(resp)
	t4 := time.Now()
	if err != nil {
		res.Err = fmt.Errorf("read: %w", err)
		return res
	}
	if n < ntpPacketSize {
		res.Err = fmt.Errorf("short reply: %d bytes", n)
		return res
	}

	leap := resp[0] >> 6
	res.Stratum = resp[1]

	if mode := resp[0] & 0x07; mode != ntpModeServer {
		res.Err = fmt.Errorf("unexpected mode %d in reply", mode)
		return res
	}
	if res.Stratum == 0 {
		res.Err = fmt.Errorf("kiss-of-death from server (code %q)", refIDString(resp[12:16]))
		return res
	}
	if res.Stratum > maxStratum {
		res.Err = fmt.Errorf("invalid stratum %d", res.Stratum)
		return res
	}
	if leap == leapNotInSync {
		res.Err = fmt.Errorf("server clock is not synchronized (leap indicator %d)", leap)
		return res
	}
	// Origin timestamp must be the t1 we sent.
	for i := range 8 {
		if resp[24+i] != req[40+i] {
			res.Err = fmt.Errorf("origin timestamp does not match request")
			return res
		}
	}

	t2 := getNTPTime(resp[32:40]) // server receive
	t3 := getNTPTime(resp[40:48]) // server transmit
	if t3.IsZero() {
		res.Err = fmt.Errorf("server returned a zero transmit timestamp")
		return res
	}

	res.Offset = (t2.Sub(t1) + t3.Sub(t4)) / 2
	res.RoundTrip = max(t4.Sub(t1)-t3.Sub(t2), 0)
	res.NTPTime = t1.Add(res.Offset).UTC()

	return res
}

// NTP timestamps are 64-bit fixed point: 32 bits of seconds since the NTP
// epoch, 32 bits of fractional second.
func putNTPTime(b []byte, t time.Time) {
	sec := uint64(t.Unix() + ntpEpochOffset)
	frac := (uint64(t.Nanosecond()) << 32) / uint64(time.Second)
	v := sec<<32 | frac
	for i := range 8 {
		b[i] = byte(v >> (56 - 8*i))
	}
}

func getNTPTime(b []byte) time.Time {
	var v uint64
	for i := range 8 {
		v = v<<8 | uint64(b[i])
	}
	if v == 0 {
		return time.Time{}
	}
	sec := int64(v>>32) - ntpEpochOffset
	nsec := (int64(v&0xFFFFFFFF) * int64(time.Second)) >> 32
	return time.Unix(sec, nsec).UTC()
}

// refIDString renders a stratum-0 reference identifier, which is a
// four-character ASCII kiss code.
func refIDString(b []byte) string {
	out := make([]byte, 0, 4)
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			out = append(out, c)
		}
	}
	return string(out)
}
