// get_host_metrics (Task #11) is the one tool in this package that does NOT
// wrap a JSON /api/* endpoint. Disk usage is collected by the dashboard's
// poller but only ever exposed on its Prometheus text-exposition scrape
// endpoint, GET /metrics (dashboard/internal/server/handlers_metrics.go's
// handleGetMetricsProm) — there is no JSON route for it, so every other
// read-only tool in this package would have no way to surface it. This file
// fetches that sibling endpoint via dashboard.Client.GetMetrics, parses the
// hand-rolled Prometheus text defensively with the standard library only (no
// new dependency — the dashboard itself avoids a prometheus client lib for
// the same no-new-deps reason), and returns a STRUCTURED result rather than
// proxying raw text through like every other tool's get() helper does. That
// is a deliberate departure from tools.go's "never re-model the response
// shape" convention: /metrics has no schema of its own (it's exposition text,
// not JSON) to preserve faithfully, so parsing it into typed fields is the
// only way to make it consumable by an LLM caller at all.
package tools

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vkatrichenko/wireguard-vpn/mcp/internal/dashboard"
)

// diskUsage is one wireguard_host_disk_percent{mount="..."} series.
type diskUsage struct {
	Mount       string  `json:"mount"`
	PercentFull float64 `json:"percent_full"`
}

// peerMetric merges the three per-peer metric families
// (wireguard_peer_last_handshake_age_seconds, wireguard_peer_rx_bytes_total,
// wireguard_peer_tx_bytes_total) keyed by their shared peer="..." label into
// one row per peer. LastHandshakeAgeSeconds is a pointer because the
// dashboard omits that series entirely for a peer that has never
// handshaked (handlers_metrics.go: "its byte counters still emit" but the
// age series does not) — nil here means exactly that, not zero seconds.
type peerMetric struct {
	Name                    string   `json:"name"`
	RxBytes                 int64    `json:"rx_bytes"`
	TxBytes                 int64    `json:"tx_bytes"`
	LastHandshakeAgeSeconds *float64 `json:"last_handshake_age_seconds,omitempty"`
}

// hostMetrics is get_host_metrics' structured output. Every optional
// (pointer) field mirrors a metric family the dashboard itself omits under
// documented conditions (handlers_metrics.go: ServiceKnown/CPUKnown/MemKnown
// gate whether the corresponding series is emitted at all) — nil here means
// "the dashboard hasn't sampled this yet," never a fabricated zero.
type hostMetrics struct {
	ServiceActive *bool        `json:"service_active,omitempty" jsonschema:"WireGuard wg-quick@wg0 systemd unit active (true) or inactive (false); omitted if the dashboard has not read systemd status yet"`
	PeersTotal    int          `json:"peers_total" jsonschema:"total WireGuard peers in the manifest"`
	PeersOnline   int          `json:"peers_online" jsonschema:"peers whose most recent handshake is within the online window"`
	Peers         []peerMetric `json:"peers" jsonschema:"per-peer rx/tx byte counters and last-handshake age, sorted by name"`
	CPUPercent    *float64     `json:"cpu_percent,omitempty" jsonschema:"host CPU utilisation percent; omitted if no /proc/stat sample has succeeded yet"`
	MemoryPercent *float64     `json:"memory_percent,omitempty" jsonschema:"host memory utilisation percent; omitted if no /proc/meminfo sample has succeeded yet"`
	Disks         []diskUsage  `json:"disks" jsonschema:"filesystem fullness percent per mount, sorted by mount"`
	ActiveAlerts  int          `json:"active_alerts" jsonschema:"number of currently-firing alerts"`
	BuildVersion  string       `json:"build_version,omitempty" jsonschema:"dashboard build version/release tag"`
	BuildSHA      string       `json:"build_sha,omitempty" jsonschema:"dashboard build git commit sha"`
	Raw           string       `json:"raw,omitempty" jsonschema:"the raw Prometheus text exposition body, for debugging a parse gap"`
}

// addHostMetricsTool registers get_host_metrics. Called from Register
// (tools.go) alongside every other read-only tool.
func addHostMetricsTool(server *mcp.Server, client *dashboard.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_host_metrics",
		Description: "Host metrics only available on the dashboard's Prometheus /metrics endpoint, not any JSON /api/* route: " +
			"per-mount disk usage percent, host CPU/memory percent, per-peer rx/tx bytes and last-handshake age, " +
			"peer/alert counts, and build version/sha. Returns parsed structured JSON, not raw Prometheus text.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, hostMetrics, error) {
		body, err := client.GetMetrics(ctx)
		if err != nil {
			return nil, hostMetrics{}, err
		}
		return nil, parseHostMetrics(string(body)), nil
	})
}

// parseHostMetrics parses the Prometheus text exposition produced by
// handleGetMetricsProm into hostMetrics. It is defensive by construction:
// every line is parsed independently, a line that doesn't match the
// expected "metric{labels} value" or "metric value" shape is skipped (not
// fatal), and any metric family not in the known switch below is ignored —
// this is what makes the parser forward-compatible with dashboard metric
// families added later without a matching MCP-side change.
func parseHostMetrics(text string) hostMetrics {
	out := hostMetrics{Raw: text}
	peers := map[string]*peerMetric{}
	disks := map[string]*diskUsage{}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, ok := parsePromLine(line)
		if !ok {
			continue
		}

		switch s.name {
		case "wireguard_service_active":
			if f, ok := s.float(); ok {
				v := f != 0
				out.ServiceActive = &v
			}
		case "wireguard_peers_total":
			if f, ok := s.float(); ok {
				out.PeersTotal = int(f)
			}
		case "wireguard_peers_online":
			if f, ok := s.float(); ok {
				out.PeersOnline = int(f)
			}
		case "wireguard_peer_last_handshake_age_seconds":
			name := s.labels["peer"]
			if name == "" {
				continue
			}
			if f, ok := s.float(); ok {
				peer(peers, name).LastHandshakeAgeSeconds = &f
			}
		case "wireguard_peer_rx_bytes_total":
			name := s.labels["peer"]
			if name == "" {
				continue
			}
			if f, ok := s.float(); ok {
				peer(peers, name).RxBytes = int64(f)
			}
		case "wireguard_peer_tx_bytes_total":
			name := s.labels["peer"]
			if name == "" {
				continue
			}
			if f, ok := s.float(); ok {
				peer(peers, name).TxBytes = int64(f)
			}
		case "wireguard_host_cpu_percent":
			if f, ok := s.float(); ok {
				out.CPUPercent = &f
			}
		case "wireguard_host_memory_percent":
			if f, ok := s.float(); ok {
				out.MemoryPercent = &f
			}
		case "wireguard_host_disk_percent":
			mount := s.labels["mount"]
			if mount == "" {
				continue
			}
			if f, ok := s.float(); ok {
				disk(disks, mount).PercentFull = f
			}
		case "wireguard_active_alerts":
			if f, ok := s.float(); ok {
				out.ActiveAlerts = int(f)
			}
		case "wireguard_build_info":
			out.BuildVersion = s.labels["version"]
			out.BuildSHA = s.labels["sha"]
		default:
			// Unknown/future metric family — ignored, not an error. This is
			// the forward-compatibility guarantee the task calls for.
		}
	}

	out.Peers = make([]peerMetric, 0, len(peers))
	for _, p := range peers {
		out.Peers = append(out.Peers, *p)
	}
	sort.Slice(out.Peers, func(i, j int) bool { return out.Peers[i].Name < out.Peers[j].Name })

	out.Disks = make([]diskUsage, 0, len(disks))
	for _, d := range disks {
		out.Disks = append(out.Disks, *d)
	}
	sort.Slice(out.Disks, func(i, j int) bool { return out.Disks[i].Mount < out.Disks[j].Mount })

	return out
}

// peer returns the *peerMetric for name, creating (and naming) it on first
// reference regardless of which of the three per-peer families is seen
// first in the text — the dashboard emits handshake-age (a subset: only
// peers with a known handshake), then rx, then tx, so first-seen order is
// not a reliable merge key on its own; keying by the peer label is.
func peer(peers map[string]*peerMetric, name string) *peerMetric {
	p, ok := peers[name]
	if !ok {
		p = &peerMetric{Name: name}
		peers[name] = p
	}
	return p
}

// disk returns the *diskUsage for mount, creating it on first reference.
func disk(disks map[string]*diskUsage, mount string) *diskUsage {
	d, ok := disks[mount]
	if !ok {
		d = &diskUsage{Mount: mount}
		disks[mount] = d
	}
	return d
}

// promSample is one parsed exposition line: a metric name, its labels (nil
// if the line had none), and the raw trailing value token (left as a string
// since most callers convert it themselves via float(), and a handful of
// callers — wireguard_build_info — never need the value at all, only the
// labels).
type promSample struct {
	name   string
	labels map[string]string
	value  string
}

// float parses s.value as a float64. A malformed or missing value reports
// ok=false so the caller can skip that single sample rather than fail the
// whole scrape — matching the dashboard emitter's own "never panic on a
// scrape" contract, applied symmetrically on the parse side.
func (s promSample) float() (float64, bool) {
	if s.value == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s.value, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parsePromLine parses one non-comment, non-blank exposition line into a
// promSample. It handles both label forms Prometheus text exposition
// allows: "metric value" and "metric{k=\"v\",k2=\"v2\"} value". ok is false
// for any line that doesn't fit either shape (missing value, unterminated
// label block, malformed label syntax) — the caller skips such lines rather
// than treating them as fatal, per this parser's defensive-by-construction
// design.
func parsePromLine(line string) (promSample, bool) {
	braceIdx := strings.IndexByte(line, '{')
	if braceIdx < 0 {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return promSample{}, false
		}
		return promSample{name: fields[0], value: fields[1]}, true
	}

	name := strings.TrimSpace(line[:braceIdx])
	closeIdx := findLabelClose(line, braceIdx)
	if closeIdx < 0 {
		return promSample{}, false
	}
	labels, err := parsePromLabels(line[braceIdx+1 : closeIdx])
	if err != nil {
		return promSample{}, false
	}
	value := strings.TrimSpace(line[closeIdx+1:])
	if value == "" {
		return promSample{}, false
	}
	// A trailing timestamp (a second whitespace-separated token) is legal
	// per the exposition format but never emitted by handleGetMetricsProm;
	// take only the first token defensively in case that ever changes.
	if fields := strings.Fields(value); len(fields) > 0 {
		value = fields[0]
	}
	return promSample{name: name, labels: labels, value: value}, true
}

// findLabelClose returns the index of the '}' matching the '{' at
// line[openIdx], scanning past any '}' that appears inside a quoted label
// value (e.g. a peer name containing a literal "}" would be escaped per
// promEscapeLabel's rules, but this scan tracks quote state regardless of
// escaping so a stray unescaped brace inside quotes still doesn't
// prematurely close the label block). Returns -1 if the block never closes.
func findLabelClose(line string, openIdx int) int {
	inQuotes := false
	escaped := false
	for i := openIdx + 1; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '\\':
			if inQuotes {
				escaped = true
			}
		case '"':
			inQuotes = !inQuotes
		case '}':
			if !inQuotes {
				return i
			}
		}
	}
	return -1
}

// parsePromLabels parses the inside of a "{...}" label block into a map,
// honoring the backslash-escapes promEscapeLabel (dashboard/internal/server/
// handlers_metrics.go) applies to label values: \\ -> \, \" -> ", \n -> a
// literal newline.
func parsePromLabels(s string) (map[string]string, error) {
	labels := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i >= len(s) {
			break
		}

		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return nil, errMalformedLabel
		}
		key := strings.TrimSpace(s[i : i+eq])
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return nil, errMalformedLabel
		}
		i++ // opening quote

		var val strings.Builder
		closed := false
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				switch s[i+1] {
				case '"':
					val.WriteByte('"')
					i += 2
					continue
				case '\\':
					val.WriteByte('\\')
					i += 2
					continue
				case 'n':
					val.WriteByte('\n')
					i += 2
					continue
				}
			}
			if c == '"' {
				closed = true
				i++
				break
			}
			val.WriteByte(c)
			i++
		}
		if !closed {
			return nil, errMalformedLabel
		}
		labels[key] = val.String()
	}
	return labels, nil
}

// errMalformedLabel is returned by parsePromLabels for any label-block
// syntax it doesn't recognize; parsePromLine treats it the same as every
// other parse failure — skip the line, don't fail the scrape.
var errMalformedLabel = errors.New("malformed prometheus label")
