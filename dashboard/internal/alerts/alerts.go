// Package alerts is the dashboard's pure, in-memory alert evaluator: an
// edge-triggered per-condition state machine that the poller drives once per
// sample tick. It owns NO I/O — every transition is a function of the inputs
// the caller passes in plus an injected clock — so the whole core is
// deterministically unit-testable without sleeping, shelling out, or touching
// /proc, systemd, or the network. Delivery is somebody else's job: Evaluate
// returns a slice of Events and the poller dispatches them through the
// notify.Notifier OFF the poll critical path (spec 007 §2, Slice 2).
//
// # State model
//
// Each watched condition is identified by a stable Key (a condition id, e.g.
// "service-down"; Slices 4+ append a per-client sub-key so one noisy peer can't
// suppress another). A Key is in exactly one of two states:
//
//	OK  ──bad input──▶  FIRING      (emits one Fire event)
//	FIRING ──good input──▶  OK      (emits one Recovery event, then re-arms)
//
// While a Key is FIRING and the input stays bad, re-notification is SUPPRESSED
// until the Cooldown elapses; once it does, a still-bad input emits a fresh
// Fire event (a reminder) and the cooldown clock restarts. Recovery re-arms the
// Key so the next OK→FIRING transition fires immediately again. This gives the
// three guarantees the spec asks for: exactly one fire on entry, no spam during
// cooldown, exactly one recovery on clear.
//
// # Restart semantics
//
// State lives only in the Evaluator's map; there is no DB and no persistence. A
// process restart therefore re-arms every Key from OK, so a condition that is
// STILL bad on the first post-restart tick fires once. This is documented and
// accepted (spec 007 §2.3) — the alternative (persisting alert state) buys
// little and complicates the store.
//
// # Extensibility
//
// The state machine in stateMachine.observe is condition-agnostic: it takes a
// boolean "firing now?" plus a human-readable detail string and drives the
// OK↔FIRING transitions. Each condition (service-down here; high-disk,
// sustained-CPU, transfer-cap in later slices) is a thin function
// that turns its slice of the Input into per-Key (firing, detail) observations
// and feeds them in. Adding a condition is "compute the booleans and call
// observe" — no rewrite of the transition logic.
package alerts

import (
	"fmt"
	"sort"
	"time"
)

// DefaultCooldown is the minimum gap between successive Fire events for a single
// still-firing condition. Defaults to 30 minutes per the functional spec; it is
// a configurable field on Evaluator (Config.Cooldown) so a later slice can wire
// an env knob without a code change. The boundary is treated as "elapsed when
// the gap is >= Cooldown" (see stateMachine.observe).
const DefaultCooldown = 30 * time.Minute

// Slice-3 condition defaults. Each is the zero-fallback for the corresponding
// Config field and is exported so main.go can document the same value beside the
// env knob that overrides it. The thresholds are percentages (0-100) matched
// against the readings the poller already gathers each tick.
const (
	// DefaultDiskThresholdPct is the fullest-filesystem percentage at or above
	// which the high-disk condition fires (>= is inclusive, so 90.0 fires).
	DefaultDiskThresholdPct = 90.0
	// DefaultCPUThresholdPct is the CPU-utilisation percentage at or above which
	// the sustained-CPU condition starts its sustain timer.
	DefaultCPUThresholdPct = 90.0
	// DefaultCPUSustain is how long CPU must stay continuously at or above the
	// threshold before the sustained-CPU condition fires. A briefer spike resets
	// the timer and never fires.
	DefaultCPUSustain = 5 * time.Minute
)

// Slice-4 per-client condition defaults. Each is the zero-fallback for the
// corresponding Config field, exported so main.go can document the same value
// beside the env knob that overrides it.
const (
	// DefaultTransferCapBytes is the cumulative-since-dashboard-start transfer (in
	// EITHER direction) at or above which the per-peer transfer-cap condition
	// fires. 50 GiB by default.
	DefaultTransferCapBytes int64 = 50 << 30
)

// Condition identifies a watched condition class. It is the stable prefix of an
// Event Key; per-client conditions (Slices 4+) suffix it with a client sub-key.
type Condition string

const (
	// ConditionServiceDown fires when the wg-quick@wg0 systemd unit is not
	// active. It is the only condition implemented in Slice 2.
	ConditionServiceDown Condition = "service-down"
	// ConditionHighDisk fires when ANY monitored filesystem is at or above the
	// disk threshold. The detail names the fullest offending mount (Slice 3).
	ConditionHighDisk Condition = "high-disk"
	// ConditionHighCPU fires when CPU utilisation stays at or above the CPU
	// threshold continuously for the sustain window — a brief spike does not
	// fire (Slice 3).
	ConditionHighCPU Condition = "high-cpu"
	// ConditionTransferCap fires when a client's cumulative-since-dashboard-start
	// transfer in either direction reaches the cap. Key "transfer-cap:<name>",
	// the only PER-CLIENT condition: its Key suffixes the client name so each
	// client gets an independent state machine (Slice 4).
	ConditionTransferCap Condition = "transfer-cap"
)

// Kind distinguishes a fire event from a recovery event on an Event.
type Kind int

const (
	// Fire marks an OK→FIRING transition or a post-cooldown re-notification.
	Fire Kind = iota
	// Recovery marks a FIRING→OK transition (emitted exactly once per clear).
	Recovery
)

// MarshalJSON renders the Kind as its human token ("FIRING"/"RECOVERED") rather
// than the raw iota int, so GET /api/alerts is self-describing — the front-end
// reads the same label the template and the webhook message use, with no
// enum-decode shared contract to keep in sync.
func (k Kind) MarshalJSON() ([]byte, error) {
	return []byte(`"` + k.String() + `"`), nil
}

// String renders the Kind for messages/logs.
func (k Kind) String() string {
	switch k {
	case Fire:
		return "FIRING"
	case Recovery:
		return "RECOVERED"
	default:
		return "UNKNOWN"
	}
}

// Event is one thing-to-notify the Evaluator hands back to the poller. It
// carries enough to build a human-readable message without any further lookup:
// the condition, whether it is a fire or a recovery, a short measured-value /
// detail string, and the tick timestamp. The poller turns it into a string via
// FormatMessage and dispatches it through the notify.Notifier.
type Event struct {
	// Condition is the condition class (e.g. service-down).
	Condition Condition
	// Key is the full state-machine key: the condition id, plus a per-client
	// sub-key in later slices. For Slice 2 conditions Key == Condition.
	Key string
	// Kind is Fire or Recovery.
	Kind Kind
	// Detail is a short human-readable measurement/context (e.g. the systemd
	// state token "inactive"). May be empty.
	Detail string
	// At is the tick timestamp (the Evaluator's injected clock), in UTC.
	At time.Time
}

// FilesystemUsage is one filesystem's fullness as the alerts package sees it.
// It is deliberately the package's OWN shape, NOT internal/disk.Mount, so the
// alerts package never imports the disk package — the poller maps disk.Mount →
// FilesystemUsage at the seam. Only the two fields the high-disk condition needs
// are carried.
type FilesystemUsage struct {
	// Mount is the mountpoint path (e.g. "/", "/var/lib/docker").
	Mount string
	// PctFull is the percentage full (0-100, one decimal as reported by disk).
	PctFull float64
}

// PeerSample is one WireGuard peer's state as the alerts package sees it. Like
// FilesystemUsage it is the package's OWN shape, NOT wg.Peer, so internal/alerts
// never imports the wg package — the poller maps wg.Peer (+ the manifest name)
// into PeerSample at the seam, skipping wg-only peers that have no client name.
// Name is the manifest client name and is the per-client sub-key for the
// per-peer transfer-cap condition.
type PeerSample struct {
	// Name is the manifest client name (e.g. "alice"). Used as the per-client
	// sub-key of the transfer-cap Key.
	Name string
	// LastHandshake is the peer's most recent handshake time, or the zero time
	// if it has never handshaked. Carried at the seam for completeness; no
	// condition currently keys off it.
	LastHandshake time.Time
	// RxBytes / TxBytes are the peer's cumulative byte counters as reported by
	// `wg show` (cumulative from before the dashboard started — the transfer-cap
	// condition subtracts a first-observation baseline to get "since start").
	RxBytes int64
	TxBytes int64
}

// Input is the per-tick snapshot the poller feeds to Evaluate. It is a plain
// value type: each field corresponds to a condition's raw reading. Slice 2 only
// populated ServiceActive; Slice 3 adds CPUPercent and DiskUsage; Slice 4 adds
// Peers. Later slices add fields here without changing the state machine.
//
// All fields are OPTIONAL: a condition whose input is absent (e.g. DiskUsage nil
// because the disk read failed this tick) simply observes "not firing" and the
// poller omits it best-effort, never fabricating a reading.
type Input struct {
	// ServiceActive is true iff the wg-quick@wg0 unit is active. The poller
	// reads it from the systemd service status each tick.
	ServiceActive bool
	// CPUPercent is host CPU utilisation (0-100) for this tick. The poller
	// threads through the value collect already computed from proc.Sample — it
	// must NOT re-sample proc, which would split the per-tick CPU delta and
	// corrupt both the chart sample and this reading. A first-sample 0 is below
	// the threshold, so it naturally keeps the sustained-CPU condition OK.
	CPUPercent float64
	// DiskUsage is the per-filesystem fullness for this tick, already filtered
	// to real block devices by the disk package. nil when the disk read failed
	// (best-effort: the high-disk condition then observes "not firing").
	DiskUsage []FilesystemUsage
	// Peers is the per-client WireGuard state for this tick, already filtered to
	// manifest clients (wg-only peers with no client name are dropped at the
	// poller seam). nil when the wg read failed: the per-peer condition then
	// observes nothing this tick, which is benign — transfer-cap keys hold their
	// FIRING/OK state and transfer baselines persist on the Evaluator.
	Peers []PeerSample
}

// Config holds the evaluator's tunables. Each zero-valued field falls back to
// its Default* const in New, so a caller can set only the knobs it cares about.
// Host is the label stamped into messages (os.Hostname() in production — a
// portable identifier, deliberately NOT the AWS IMDSv2 instance id, so the
// dashboard stays cloud-agnostic).
type Config struct {
	Cooldown time.Duration
	Host     string

	// DiskThresholdPct: the high-disk condition fires when any filesystem's
	// PctFull is >= this. Defaults to DefaultDiskThresholdPct when <= 0.
	DiskThresholdPct float64
	// CPUThresholdPct: the sustained-CPU condition arms when CPUPercent is >=
	// this. Defaults to DefaultCPUThresholdPct when <= 0.
	CPUThresholdPct float64
	// CPUSustain: how long CPU must stay at/above the threshold before the
	// sustained-CPU condition fires. Defaults to DefaultCPUSustain when <= 0.
	CPUSustain time.Duration

	// TransferCapBytes: the per-peer transfer-cap condition fires when a peer's
	// rx OR tx since dashboard start reaches this many bytes. Defaults to
	// DefaultTransferCapBytes when <= 0.
	TransferCapBytes int64
}

// Evaluator is the stateful core. Construct with New. It is NOT safe for
// concurrent use — the poller drives Evaluate from a single goroutine (the
// sample loop); dispatch of the returned Events happens elsewhere, so the
// Evaluator's map is never touched concurrently. Keeping it single-threaded
// avoids a mutex on the hot path and matches how the poller already serialises
// its collect work.
type Evaluator struct {
	cooldown         time.Duration
	host             string
	diskThresholdPct float64
	cpuThresholdPct  float64
	cpuSustain       time.Duration
	transferCapBytes int64
	states           map[string]*stateMachine

	// cpuHighSince is the timestamp CPU first crossed cpuThresholdPct in the
	// current high run, or the zero time when the most recent tick was below it.
	// It lives on the Evaluator (not the condition-agnostic stateMachine) because
	// the sustain window is a property of THIS condition's input history, fed in
	// as a single firingNow boolean to the shared observe funnel. Reset to zero
	// the moment a tick drops below threshold, so a spike can never accumulate.
	cpuHighSince time.Time

	// Per-peer state, keyed by client name — the same "extra per-condition state
	// lives on the Evaluator" pattern as cpuHighSince (the shared stateMachine
	// stays condition-agnostic). This map persists across ticks for the life of
	// the process.
	//
	// transferBaseline[name] is the peer's rx/tx counter pair captured at FIRST
	// observation, so (current - baseline) is the traffic "since dashboard start"
	// (the wg counters are cumulative from before we started). On a counter reset
	// (current < baseline — interface/peer re-add or wrap) the baseline is reset
	// to the current value and the transfer-cap state machine re-armed.
	transferBaseline map[string]transferCounters
}

// transferCounters is a peer's rx/tx byte pair, used for the transfer-cap
// baseline.
type transferCounters struct {
	rx int64
	tx int64
}

// New returns an Evaluator with the given Config, filling each zero-valued knob
// from its Default* const.
func New(cfg Config) *Evaluator {
	cooldown := cfg.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	diskThreshold := cfg.DiskThresholdPct
	if diskThreshold <= 0 {
		diskThreshold = DefaultDiskThresholdPct
	}
	cpuThreshold := cfg.CPUThresholdPct
	if cpuThreshold <= 0 {
		cpuThreshold = DefaultCPUThresholdPct
	}
	cpuSustain := cfg.CPUSustain
	if cpuSustain <= 0 {
		cpuSustain = DefaultCPUSustain
	}
	transferCap := cfg.TransferCapBytes
	if transferCap <= 0 {
		transferCap = DefaultTransferCapBytes
	}
	return &Evaluator{
		cooldown:         cooldown,
		host:             cfg.Host,
		diskThresholdPct: diskThreshold,
		cpuThresholdPct:  cpuThreshold,
		cpuSustain:       cpuSustain,
		transferCapBytes: transferCap,
		states:           make(map[string]*stateMachine),
		transferBaseline: make(map[string]transferCounters),
	}
}

// Host returns the configured host label (for callers that want to log it).
func (e *Evaluator) Host() string { return e.host }

// Evaluate runs every condition against in for the tick whose wall-clock time is
// now, and returns the Events that resulted (zero or more). now is injected so
// cooldown/transition logic is deterministic in tests; it is normalised to UTC
// so Event.At is consistent regardless of the caller's location. The returned
// slice is freshly allocated each call and is safe for the caller to retain.
//
// Determinism: when multiple conditions fire on the same tick the events are
// returned in a stable (Key-sorted) order so tests don't depend on map
// iteration order.
func (e *Evaluator) Evaluate(now time.Time, in Input) []Event {
	now = now.UTC()
	var events []Event

	// Each condition is a thin "compute (firingNow, detail) then observe" step;
	// the shared funnel owns the edge-trigger / cooldown / recovery rules.

	// service-down (Slice 2).
	events = e.observe(events, string(ConditionServiceDown), ConditionServiceDown, !in.ServiceActive, serviceDownDetail(in), now)

	// high-disk (Slice 3): fires when ANY filesystem is at/over the threshold.
	diskFiring, diskDetail := e.diskObservation(in)
	events = e.observe(events, string(ConditionHighDisk), ConditionHighDisk, diskFiring, diskDetail, now)

	// sustained-CPU (Slice 3): the sustain window is tracked here (cpuHighSince)
	// and collapsed to a single firingNow boolean before the funnel.
	cpuFiring, cpuDetail := e.cpuObservation(in, now)
	events = e.observe(events, string(ConditionHighCPU), ConditionHighCPU, cpuFiring, cpuDetail, now)

	// Per-client condition (Slice 4). Each peer gets an independent state machine
	// via a per-name Key, so one noisy peer never suppresses another. The
	// Input.Peers slice is already manifest-filtered by the poller.
	for _, peer := range in.Peers {
		capFiring, capDetail := e.transferCapObservation(peer)
		events = e.observe(events, string(ConditionTransferCap)+":"+peer.Name, ConditionTransferCap, capFiring, capDetail, now)
	}

	sort.Slice(events, func(i, j int) bool { return events[i].Key < events[j].Key })
	return events
}

// transferCapObservation returns the transfer-cap condition's (firingNow, detail)
// for one peer. The baseline is captured at FIRST observation so the running
// total is "since dashboard start"; it fires when rx OR tx since the baseline
// reaches transferCapBytes.
//
// Counter-reset recovery (the agreed semantics): cumulative counters only
// increase, so a fired alert stays FIRING (cooldown-suppressed) and never
// naturally recovers. The ONLY recovery path is a counter reset — if the
// current value drops BELOW the baseline, the wg counter was reset (interface or
// peer re-add) or wrapped: we re-baseline to the current value, which restarts
// the running total from zero and (because the new total is below the cap) lets
// the shared state machine emit a Recovery and re-arm.
func (e *Evaluator) transferCapObservation(peer PeerSample) (bool, string) {
	base, ok := e.transferBaseline[peer.Name]
	if !ok {
		base = transferCounters{rx: peer.RxBytes, tx: peer.TxBytes}
		e.transferBaseline[peer.Name] = base
	}
	if peer.RxBytes < base.rx || peer.TxBytes < base.tx {
		// Counter reset/wrap: restart the running total from the current value.
		base = transferCounters{rx: peer.RxBytes, tx: peer.TxBytes}
		e.transferBaseline[peer.Name] = base
	}
	rxDelta := peer.RxBytes - base.rx
	txDelta := peer.TxBytes - base.tx
	if rxDelta >= e.transferCapBytes {
		return true, fmt.Sprintf("%s downloaded %s", peer.Name, formatBytes(rxDelta))
	}
	if txDelta >= e.transferCapBytes {
		return true, fmt.Sprintf("%s uploaded %s", peer.Name, formatBytes(txDelta))
	}
	return false, fmt.Sprintf("%s within transfer cap", peer.Name)
}

// formatBytes renders a byte count as a human-readable binary-unit string (e.g.
// "51.2 GiB"). Kept tiny and std-lib only — the detail string is for humans, not
// parsing, so one decimal place at the largest fitting unit is enough.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// diskObservation reduces the per-tick filesystem readings to the high-disk
// condition's (firingNow, detail). It fires when the fullest monitored mount is
// at or above diskThresholdPct (>= is inclusive), and the detail names that
// fullest offending mount and its percent (e.g. "/ at 93.4%"). When no
// filesystem is over (or DiskUsage is nil because the disk read failed this
// tick), it reports not-firing with a benign detail used on recovery.
func (e *Evaluator) diskObservation(in Input) (bool, string) {
	var worst FilesystemUsage
	found := false
	for _, fs := range in.DiskUsage {
		if fs.PctFull >= e.diskThresholdPct && (!found || fs.PctFull > worst.PctFull) {
			worst = fs
			found = true
		}
	}
	if !found {
		return false, "all filesystems below threshold"
	}
	return true, fmt.Sprintf("%s at %.1f%%", worst.Mount, worst.PctFull)
}

// cpuObservation tracks the sustain window for the sustained-CPU condition and
// returns its (firingNow, detail). It advances cpuHighSince: set to now on the
// first tick at/above the threshold, held across subsequent high ticks, and
// reset to zero the instant a tick drops below — so a transient spike never
// accumulates enough run to fire. firingNow is true only once the run has lasted
// at least cpuSustain (>= is inclusive of the boundary tick). The first-sample
// CPUPercent==0 is below threshold and naturally keeps the run reset.
func (e *Evaluator) cpuObservation(in Input, now time.Time) (bool, string) {
	if in.CPUPercent < e.cpuThresholdPct {
		e.cpuHighSince = time.Time{}
		return false, fmt.Sprintf("CPU %.0f%% below threshold", in.CPUPercent)
	}
	if e.cpuHighSince.IsZero() {
		e.cpuHighSince = now
	}
	if now.Sub(e.cpuHighSince) >= e.cpuSustain {
		return true, fmt.Sprintf("CPU %.0f%% for >%s", in.CPUPercent, e.cpuSustain)
	}
	// Over the threshold but not yet sustained long enough — still OK.
	return false, fmt.Sprintf("CPU %.0f%% high but not yet sustained", in.CPUPercent)
}

// observe advances the state machine for one key and appends any resulting
// Event. It is the single funnel every condition flows through, so the
// edge-trigger / cooldown / recovery rules live in exactly one place.
//
// It also records the per-key fields the in-UI active-alerts view (Slice 5)
// needs: cond + the latest detail are stashed on the stateMachine each tick so
// Active() can report them WITHOUT re-deriving anything, and firingSince is set
// on the OK→FIRING edge (not bumped on a cooldown re-notify) and cleared on
// recovery so the UI's "firing since" age is stable.
func (e *Evaluator) observe(dst []Event, key string, cond Condition, firingNow bool, detail string, now time.Time) []Event {
	sm := e.states[key]
	if sm == nil {
		sm = &stateMachine{}
		e.states[key] = sm
	}
	sm.cond = cond
	sm.lastDetail = detail

	wasFiring := sm.firing
	kind, emit := sm.observe(firingNow, now, e.cooldown)

	// firingSince tracks the ORIGINAL fire time for the active view. Set it only
	// on the OK→FIRING edge so a cooldown re-notify (still FIRING) doesn't bump
	// it; clear it on the FIRING→OK edge so a recovered key reports no since.
	if !wasFiring && sm.firing {
		sm.firingSince = now
	} else if wasFiring && !sm.firing {
		sm.firingSince = time.Time{}
	}

	if !emit {
		return dst
	}
	return append(dst, Event{
		Condition: cond,
		Key:       key,
		Kind:      kind,
		Detail:    detail,
		At:        now,
	})
}

// Active returns the currently-FIRING conditions as a snapshot for the in-UI
// active-alerts view. It is a READ of the evaluator's own state and MUST be
// called only from the poller goroutine that drives Evaluate — never from an
// HTTP goroutine (the Evaluator is not concurrency-safe; the StatusHolder is the
// safe seam to the server). Each entry carries the condition, the last detail
// string observed for the key, and the original fire time (firingSince). The
// result is Key-sorted for deterministic rendering and tests.
func (e *Evaluator) Active() []ActiveAlert {
	out := make([]ActiveAlert, 0, len(e.states))
	for key, sm := range e.states {
		if !sm.firing {
			continue
		}
		out = append(out, ActiveAlert{
			Condition: sm.cond,
			Key:       key,
			Detail:    sm.lastDetail,
			Since:     sm.firingSince,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// serviceDownDetail renders the measured-value string for the service-down
// condition: when firing, it's useful to know it's the wg-quick unit; when
// recovering, the same context reads naturally ("service active again").
func serviceDownDetail(in Input) string {
	if in.ServiceActive {
		return "wg-quick@wg0 active"
	}
	return "wg-quick@wg0 not active"
}

// FormatMessage renders an Event into the human-readable line the notifier
// delivers: condition, fire-vs-recover, detail, host, and timestamp. Kept as a
// free function (not an Event method) so the host label — which lives on the
// Evaluator, not the Event — is an explicit argument and the formatting is
// trivially testable in isolation.
//
// Example:
//
//	[FIRING] service-down on wg-host-1: wg-quick@wg0 not active (2026-06-25T10:23:15Z)
func FormatMessage(ev Event, host string) string {
	if host == "" {
		host = "unknown-host"
	}
	ts := ev.At.UTC().Format(time.RFC3339)
	if ev.Detail == "" {
		return fmt.Sprintf("[%s] %s on %s (%s)", ev.Kind, ev.Condition, host, ts)
	}
	return fmt.Sprintf("[%s] %s on %s: %s (%s)", ev.Kind, ev.Condition, host, ev.Detail, ts)
}

// stateMachine is the per-key OK↔FIRING core. firing is the current state;
// lastFire is the timestamp of the most recent Fire event emitted while in the
// FIRING state, used to enforce the cooldown. A zero-value stateMachine is a
// valid OK/never-fired machine.
//
// The remaining fields exist only to back Evaluator.Active() for the in-UI
// view; they do NOT affect the transition logic in observe. cond/lastDetail are
// refreshed each tick by the funnel; firingSince is the original OK→FIRING time
// (set/cleared by the funnel on the edges, stable across cooldown re-notifies).
type stateMachine struct {
	firing      bool
	lastFire    time.Time
	cond        Condition
	lastDetail  string
	firingSince time.Time
}

// observe advances the machine for one tick and reports whether an Event should
// be emitted and of which Kind.
//
//   - OK + bad        → FIRING, emit Fire (record lastFire).
//   - FIRING + bad    → stay FIRING; emit a reminder Fire only once the cooldown
//     has elapsed since lastFire (gap >= cooldown), restarting the clock.
//   - FIRING + good   → OK, emit Recovery, re-arm (clear lastFire).
//   - OK + good       → stay OK, emit nothing.
//
// The cooldown boundary is inclusive of "elapsed": a tick exactly cooldown after
// lastFire re-notifies. Tests drive just-under (suppressed) and at/over
// (re-notified) around this boundary.
func (s *stateMachine) observe(firingNow bool, now time.Time, cooldown time.Duration) (Kind, bool) {
	if firingNow {
		if !s.firing {
			s.firing = true
			s.lastFire = now
			return Fire, true
		}
		// Already firing: re-notify only after the cooldown has elapsed.
		if now.Sub(s.lastFire) >= cooldown {
			s.lastFire = now
			return Fire, true
		}
		return Fire, false
	}

	// Not firing now.
	if s.firing {
		s.firing = false
		s.lastFire = time.Time{}
		return Recovery, true
	}
	return Recovery, false
}
