package service

import (
	"container/heap"
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	openAIAccountScheduleLayerPreviousResponse = "previous_response_id"
	openAIAccountScheduleLayerSessionSticky    = "session_hash"
	openAIAccountScheduleLayerLoadBalance      = "load_balance"
)

type OpenAIAccountScheduleRequest struct {
	GroupID            *int64
	SessionHash        string
	StickyAccountID    int64
	PreviousResponseID string
	RequestedModel     string
	RequiredTransport  OpenAIUpstreamTransport
	ExcludedIDs        map[int64]struct{}
}

type OpenAIAccountScheduleDecision struct {
	Layer               string
	StickyPreviousHit   bool
	StickySessionHit    bool
	CandidateCount      int
	TopK                int
	LatencyMs           int64
	LoadSkew            float64
	SelectedAccountID   int64
	SelectedAccountType string
}

type OpenAIAccountSchedulerMetricsSnapshot struct {
	SelectTotal                   int64
	StickyPreviousHitTotal        int64
	StickySessionHitTotal         int64
	LoadBalanceSelectTotal        int64
	AccountSwitchTotal            int64
	SchedulerLatencyMsTotal       int64
	SchedulerLatencyMsAvg         float64
	StickyHitRatio                float64
	AccountSwitchRate             float64
	LoadSkewAvg                   float64
	RuntimeStatsAccountCount      int
	CircuitBreakerOpenTotal       int64
	CircuitBreakerRecoverTotal    int64
	StickyReleaseErrorTotal       int64
	StickyReleaseCircuitOpenTotal int64
}

type OpenAIAccountScheduler interface {
	Select(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error)
	ReportResult(accountID int64, success bool, firstTokenMs *int, model string, ttftMs float64)
	ReportSwitch()
	SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot
}

type openAIAccountSchedulerMetrics struct {
	selectTotal                   atomic.Int64
	stickyPreviousHitTotal        atomic.Int64
	stickySessionHitTotal         atomic.Int64
	loadBalanceSelectTotal        atomic.Int64
	accountSwitchTotal            atomic.Int64
	latencyMsTotal                atomic.Int64
	loadSkewMilliTotal            atomic.Int64
	circuitBreakerOpenTotal       atomic.Int64
	circuitBreakerRecoverTotal    atomic.Int64
	stickyReleaseErrorTotal       atomic.Int64
	stickyReleaseCircuitOpenTotal atomic.Int64
}

func (m *openAIAccountSchedulerMetrics) recordSelect(decision OpenAIAccountScheduleDecision) {
	if m == nil {
		return
	}
	m.selectTotal.Add(1)
	m.latencyMsTotal.Add(decision.LatencyMs)
	m.loadSkewMilliTotal.Add(int64(math.Round(decision.LoadSkew * 1000)))
	if decision.StickyPreviousHit {
		m.stickyPreviousHitTotal.Add(1)
	}
	if decision.StickySessionHit {
		m.stickySessionHitTotal.Add(1)
	}
	if decision.Layer == openAIAccountScheduleLayerLoadBalance {
		m.loadBalanceSelectTotal.Add(1)
	}
}

func (m *openAIAccountSchedulerMetrics) recordSwitch() {
	if m == nil {
		return
	}
	m.accountSwitchTotal.Add(1)
}

type openAIAccountRuntimeStats struct {
	accounts        sync.Map
	circuitBreakers sync.Map // accountID → *accountCircuitBreaker
	accountCount    atomic.Int64
	cleanupCounter  atomic.Int64 // report call counter for periodic cleanup
}

// ---------------------------------------------------------------------------
// Account-level Circuit Breaker (three-state: CLOSED → OPEN → HALF_OPEN)
// ---------------------------------------------------------------------------

const (
	circuitBreakerStateClosed   int32 = 0
	circuitBreakerStateOpen     int32 = 1
	circuitBreakerStateHalfOpen int32 = 2

	// Defaults (used when config values are zero/unset)
	defaultCircuitBreakerFailThreshold = 5
	defaultCircuitBreakerCooldownSec   = 30
	defaultCircuitBreakerHalfOpenMax   = 2
)

type accountCircuitBreaker struct {
	state            atomic.Int32 // circuitBreakerState*
	consecutiveFails atomic.Int32
	lastFailureNano  atomic.Int64 // time.Now().UnixNano()
	halfOpenInFlight atomic.Int32 // current in-flight probes (decremented by release)
	halfOpenAdmitted atomic.Int32 // total probes admitted this half-open cycle (never decremented by release)
	halfOpenSuccess  atomic.Int32
}

// allow returns true if the circuit breaker allows a request to pass through.
func (cb *accountCircuitBreaker) allow(cooldown time.Duration, halfOpenMax int) bool {
	switch cb.state.Load() {
	case circuitBreakerStateClosed:
		return true
	case circuitBreakerStateOpen:
		lastFail := time.Unix(0, cb.lastFailureNano.Load())
		if time.Since(lastFail) <= cooldown {
			return false
		}
		// Cooldown elapsed — attempt transition to HALF_OPEN.
		// Reset counters before CAS to avoid a window where another goroutine
		// sees HALF_OPEN but stale counter values.
		cb.halfOpenInFlight.Store(0)
		cb.halfOpenAdmitted.Store(0)
		cb.halfOpenSuccess.Store(0)
		cb.state.CompareAndSwap(circuitBreakerStateOpen, circuitBreakerStateHalfOpen)
		// Either we transitioned or another goroutine did; fall through to
		// HALF_OPEN gate below.
		return cb.allowHalfOpen(halfOpenMax)
	case circuitBreakerStateHalfOpen:
		return cb.allowHalfOpen(halfOpenMax)
	default:
		return true
	}
}

func (cb *accountCircuitBreaker) isHalfOpen() bool {
	if cb == nil {
		return false
	}
	return cb.state.Load() == circuitBreakerStateHalfOpen
}

// releaseHalfOpenPermit releases one HALF_OPEN probe permit when a candidate
// passed filtering but was not actually selected to execute a request.
func (cb *accountCircuitBreaker) releaseHalfOpenPermit() {
	if cb == nil || cb.state.Load() != circuitBreakerStateHalfOpen {
		return
	}
	for {
		cur := cb.halfOpenInFlight.Load()
		if cur <= 0 {
			return
		}
		if cb.halfOpenInFlight.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

func (cb *accountCircuitBreaker) allowHalfOpen(halfOpenMax int) bool {
	for {
		cur := cb.halfOpenInFlight.Load()
		if int(cur) >= halfOpenMax {
			return false
		}
		if cb.halfOpenInFlight.CompareAndSwap(cur, cur+1) {
			cb.halfOpenAdmitted.Add(1)
			return true
		}
	}
}

// recordSuccess is called when a request succeeds.
func (cb *accountCircuitBreaker) recordSuccess() {
	cb.consecutiveFails.Store(0)
	if cb.state.Load() == circuitBreakerStateHalfOpen {
		newSucc := cb.halfOpenSuccess.Add(1)
		// Compare against halfOpenAdmitted (total probes ever admitted in
		// this half-open cycle). Unlike halfOpenInFlight, this is never
		// decremented by releaseHalfOpenPermit, so the recovery threshold
		// remains stable regardless of candidate filtering outcomes.
		admitted := cb.halfOpenAdmitted.Load()
		if newSucc >= admitted && admitted > 0 {
			if cb.state.CompareAndSwap(circuitBreakerStateHalfOpen, circuitBreakerStateClosed) {
				cb.halfOpenInFlight.Store(0)
				cb.halfOpenAdmitted.Store(0)
				cb.halfOpenSuccess.Store(0)
			}
		}
	}
}

// recordFailure is called when a request fails.
func (cb *accountCircuitBreaker) recordFailure(threshold int) {
	cb.lastFailureNano.Store(time.Now().UnixNano())
	newFails := cb.consecutiveFails.Add(1)

	switch cb.state.Load() {
	case circuitBreakerStateClosed:
		if int(newFails) >= threshold {
			cb.state.CompareAndSwap(circuitBreakerStateClosed, circuitBreakerStateOpen)
		}
	case circuitBreakerStateHalfOpen:
		if cb.state.CompareAndSwap(circuitBreakerStateHalfOpen, circuitBreakerStateOpen) {
			cb.halfOpenInFlight.Store(0)
			cb.halfOpenAdmitted.Store(0)
			cb.halfOpenSuccess.Store(0)
		}
	}
}

// isOpen returns true if the circuit breaker is currently in OPEN state.
func (cb *accountCircuitBreaker) isOpen() bool {
	return cb.state.Load() == circuitBreakerStateOpen
}

// stateString returns a human-readable state name.
func (cb *accountCircuitBreaker) stateString() string {
	switch cb.state.Load() {
	case circuitBreakerStateClosed:
		return "CLOSED"
	case circuitBreakerStateOpen:
		return "OPEN"
	case circuitBreakerStateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// loadCircuitBreaker returns the CB for accountID if it exists, or nil.
// Use this on hot paths (e.g. candidate filtering) to avoid allocating CB
// objects for accounts that have never received a report.
func (s *openAIAccountRuntimeStats) loadCircuitBreaker(accountID int64) *accountCircuitBreaker {
	if val, ok := s.circuitBreakers.Load(accountID); ok {
		if cb, _ := val.(*accountCircuitBreaker); cb != nil {
			return cb
		}
	}
	return nil
}

func (s *openAIAccountRuntimeStats) getCircuitBreaker(accountID int64) *accountCircuitBreaker {
	if val, ok := s.circuitBreakers.Load(accountID); ok {
		if cb, _ := val.(*accountCircuitBreaker); cb != nil {
			return cb
		}
	}
	cb := &accountCircuitBreaker{}
	actual, _ := s.circuitBreakers.LoadOrStore(accountID, cb)
	if existing, _ := actual.(*accountCircuitBreaker); existing != nil {
		return existing
	}
	return cb
}

func (s *openAIAccountRuntimeStats) isCircuitOpen(accountID int64) bool {
	val, ok := s.circuitBreakers.Load(accountID)
	if !ok {
		return false
	}
	cb, _ := val.(*accountCircuitBreaker)
	if cb == nil {
		return false
	}
	return cb.isOpen()
}

// ---------------------------------------------------------------------------
// Dual-EWMA: fast (α=0.5) reacts quickly to degradation; slow (α=0.1)
// stabilises over many samples. The pessimistic envelope max(fast,slow) means
// we *sense* errors fast but only *confirm* recovery slowly.
// ---------------------------------------------------------------------------

const (
	dualEWMAAlphaFast = 0.5
	dualEWMAAlphaSlow = 0.1

	// Per-model TTFT defaults
	defaultPerModelTTFTMaxModels = 100
	defaultPerModelTTFTTTL       = 30 * time.Minute
)

// dualEWMA tracks a [0,1] signal (e.g. error rate) with two speeds.
type dualEWMA struct {
	fastBits    atomic.Uint64 // α = dualEWMAAlphaFast, reacts in ~3 requests
	slowBits    atomic.Uint64 // α = dualEWMAAlphaSlow, stabilises over ~50 requests
	sampleCount atomic.Int64  // total samples received; used for cold-start guard
}

// dualEWMAMinSamples is the minimum number of samples required before the
// EWMA error rate is considered reliable for decision-making (e.g. sticky
// release). This prevents a single failure on a fresh account from yielding
// an artificially high error rate.
const dualEWMAMinSamples = 5

func (d *dualEWMA) update(sample float64) {
	updateEWMAAtomic(&d.fastBits, sample, dualEWMAAlphaFast)
	updateEWMAAtomic(&d.slowBits, sample, dualEWMAAlphaSlow)
	d.sampleCount.Add(1)
}

// isWarmedUp returns true when enough samples have been collected for the
// EWMA value to be meaningful.
func (d *dualEWMA) isWarmedUp() bool {
	return d.sampleCount.Load() >= dualEWMAMinSamples
}

// value returns the pessimistic envelope: max(fast, slow).
func (d *dualEWMA) value() float64 {
	fast := math.Float64frombits(d.fastBits.Load())
	slow := math.Float64frombits(d.slowBits.Load())
	if fast >= slow {
		return fast
	}
	return slow
}

func (d *dualEWMA) fastValue() float64 {
	return math.Float64frombits(d.fastBits.Load())
}

func (d *dualEWMA) slowValue() float64 {
	return math.Float64frombits(d.slowBits.Load())
}

// dualEWMATTFT is like dualEWMA but handles the NaN-initialised first-sample
// case required by TTFT tracking.
type dualEWMATTFT struct {
	fastBits atomic.Uint64 // α = dualEWMAAlphaFast
	slowBits atomic.Uint64 // α = dualEWMAAlphaSlow
}

// initNaN stores NaN into both channels. Called once at allocation time.
func (d *dualEWMATTFT) initNaN() {
	nanBits := math.Float64bits(math.NaN())
	d.fastBits.Store(nanBits)
	d.slowBits.Store(nanBits)
}

func (d *dualEWMATTFT) update(sample float64) {
	sampleBits := math.Float64bits(sample)
	// fast channel
	for {
		oldBits := d.fastBits.Load()
		oldValue := math.Float64frombits(oldBits)
		if math.IsNaN(oldValue) {
			if d.fastBits.CompareAndSwap(oldBits, sampleBits) {
				break
			}
			continue
		}
		newValue := dualEWMAAlphaFast*sample + (1-dualEWMAAlphaFast)*oldValue
		if d.fastBits.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			break
		}
	}
	// slow channel
	for {
		oldBits := d.slowBits.Load()
		oldValue := math.Float64frombits(oldBits)
		if math.IsNaN(oldValue) {
			if d.slowBits.CompareAndSwap(oldBits, sampleBits) {
				break
			}
			continue
		}
		newValue := dualEWMAAlphaSlow*sample + (1-dualEWMAAlphaSlow)*oldValue
		if d.slowBits.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			break
		}
	}
}

// value returns (pessimistic TTFT, hasTTFT). If both channels are still NaN
// the caller gets (0, false).
func (d *dualEWMATTFT) value() (float64, bool) {
	fast := math.Float64frombits(d.fastBits.Load())
	slow := math.Float64frombits(d.slowBits.Load())
	fastOK := !math.IsNaN(fast)
	slowOK := !math.IsNaN(slow)
	switch {
	case fastOK && slowOK:
		if fast >= slow {
			return fast, true
		}
		return slow, true
	case fastOK:
		return fast, true
	case slowOK:
		return slow, true
	default:
		return 0, false
	}
}

func (d *dualEWMATTFT) fastValue() float64 {
	return math.Float64frombits(d.fastBits.Load())
}

func (d *dualEWMATTFT) slowValue() float64 {
	return math.Float64frombits(d.slowBits.Load())
}

// ---------------------------------------------------------------------------
// Load Trend Tracker (ring-buffer linear regression)
// ---------------------------------------------------------------------------

const loadTrendRingSize = 10

// loadTrendTracker maintains a fixed-size ring buffer of (timestamp, loadRate)
// samples and computes the ordinary-least-squares slope to predict whether
// an account's load is rising, falling, or stable.
type loadTrendTracker struct {
	mu      sync.Mutex
	samples [loadTrendRingSize]float64 // ring buffer of loadRate values
	times   [loadTrendRingSize]int64   // timestamps in UnixNano
	head    int                        // next write position
	count   int                        // number of valid samples (0..loadTrendRingSize)
}

// record pushes a loadRate sample with the current wall-clock timestamp.
func (t *loadTrendTracker) record(loadRate float64) {
	t.recordAt(loadRate, time.Now().UnixNano())
}

// recordAt pushes a loadRate sample with an explicit timestamp (for testing).
func (t *loadTrendTracker) recordAt(loadRate float64, tsNano int64) {
	t.mu.Lock()
	t.samples[t.head] = loadRate
	t.times[t.head] = tsNano
	t.head = (t.head + 1) % loadTrendRingSize
	if t.count < loadTrendRingSize {
		t.count++
	}
	t.mu.Unlock()
}

// slope computes the simple linear regression slope of loadRate over time.
//
//	slope = (N*Sigma(xi*yi) - Sigma(xi)*Sigma(yi)) / (N*Sigma(xi^2) - (Sigma(xi))^2)
//
// where xi = seconds elapsed since the oldest sample, yi = loadRate.
// Returns 0 if fewer than 2 samples are available or if all timestamps are
// identical (degenerate case).
func (t *loadTrendTracker) slope() float64 {
	t.mu.Lock()
	n := t.count
	if n < 2 {
		t.mu.Unlock()
		return 0
	}

	// Copy data under lock; computation happens outside.
	var localSamples [loadTrendRingSize]float64
	var localTimes [loadTrendRingSize]int64
	copy(localSamples[:], t.samples[:])
	copy(localTimes[:], t.times[:])
	head := t.head
	t.mu.Unlock()

	// Identify oldest entry index.
	oldest := head // head points to the next write pos; for a full ring it's the oldest entry.
	if n < loadTrendRingSize {
		oldest = 0
	}
	t0 := localTimes[oldest]

	var sumX, sumY, sumXY, sumX2 float64
	for i := 0; i < n; i++ {
		idx := (oldest + i) % loadTrendRingSize
		xi := float64(localTimes[idx]-t0) / 1e9 // relative seconds
		yi := localSamples[idx]
		sumX += xi
		sumY += yi
		sumXY += xi * yi
		sumX2 += xi * xi
	}

	nf := float64(n)
	denom := nf*sumX2 - sumX*sumX
	if denom == 0 {
		// All timestamps identical (or single sample) — no meaningful slope.
		return 0
	}
	return (nf*sumXY - sumX*sumY) / denom
}

type openAIAccountRuntimeStat struct {
	errorRate           dualEWMA
	ttft                dualEWMATTFT
	modelTTFT           sync.Map // key = model name (string), value = *dualEWMATTFT
	modelTTFTLastUpdate sync.Map // key = model name (string), value = int64 (unix nano)
	loadTrend           loadTrendTracker
	lastReportNano      atomic.Int64 // last report timestamp for GC
}

func newOpenAIAccountRuntimeStats() *openAIAccountRuntimeStats {
	return &openAIAccountRuntimeStats{}
}

// loadExisting returns the stat for accountID if it exists, or nil.
// Unlike loadOrCreate, this never allocates a new stat.
func (s *openAIAccountRuntimeStats) loadExisting(accountID int64) *openAIAccountRuntimeStat {
	if value, ok := s.accounts.Load(accountID); ok {
		stat, _ := value.(*openAIAccountRuntimeStat)
		return stat
	}
	return nil
}

func (s *openAIAccountRuntimeStats) loadOrCreate(accountID int64) *openAIAccountRuntimeStat {
	if value, ok := s.accounts.Load(accountID); ok {
		stat, _ := value.(*openAIAccountRuntimeStat)
		if stat != nil {
			return stat
		}
	}

	stat := &openAIAccountRuntimeStat{}
	stat.ttft.initNaN()
	actual, loaded := s.accounts.LoadOrStore(accountID, stat)
	if !loaded {
		s.accountCount.Add(1)
		return stat
	}
	existing, _ := actual.(*openAIAccountRuntimeStat)
	if existing != nil {
		return existing
	}
	return stat
}

// getOrCreateModelTTFT returns the per-model TTFT tracker, creating it if
// it does not exist yet. Uses the LoadOrStore pattern for thread safety.
func (stat *openAIAccountRuntimeStat) getOrCreateModelTTFT(model string) *dualEWMATTFT {
	if val, ok := stat.modelTTFT.Load(model); ok {
		if d, _ := val.(*dualEWMATTFT); d != nil {
			return d
		}
	}
	d := &dualEWMATTFT{}
	d.initNaN()
	actual, _ := stat.modelTTFT.LoadOrStore(model, d)
	if existing, _ := actual.(*dualEWMATTFT); existing != nil {
		return existing
	}
	return d
}

// reportModelTTFT updates both the per-model and global TTFT trackers.
func (stat *openAIAccountRuntimeStat) reportModelTTFT(model string, sampleMs float64) {
	if model == "" || sampleMs <= 0 {
		return
	}
	d := stat.getOrCreateModelTTFT(model)
	d.update(sampleMs)
	stat.modelTTFTLastUpdate.Store(model, time.Now().UnixNano())
	// Also update the global TTFT so that callers without a model still
	// see a reasonable aggregate.
	stat.ttft.update(sampleMs)
}

// modelTTFTValue returns the per-model TTFT value if a tracker exists and has
// received at least one sample. Otherwise returns (0, false).
func (stat *openAIAccountRuntimeStat) modelTTFTValue(model string) (float64, bool) {
	if model == "" {
		return 0, false
	}
	val, ok := stat.modelTTFT.Load(model)
	if !ok {
		return 0, false
	}
	d, _ := val.(*dualEWMATTFT)
	if d == nil {
		return 0, false
	}
	return d.value()
}

// cleanupStaleTTFT removes per-model TTFT entries that have not been updated
// within ttl, and enforces a hard cap of maxModels entries. Oldest entries
// are evicted first when the cap is exceeded.
func (stat *openAIAccountRuntimeStat) cleanupStaleTTFT(ttl time.Duration, maxModels int) {
	now := time.Now().UnixNano()
	cutoff := now - int64(ttl)

	// First pass: delete stale entries.
	stat.modelTTFTLastUpdate.Range(func(key, value any) bool {
		model, _ := key.(string)
		ts, _ := value.(int64)
		if ts < cutoff {
			stat.modelTTFT.Delete(model)
			stat.modelTTFTLastUpdate.Delete(model)
		}
		return true
	})

	if maxModels <= 0 {
		return
	}

	// Second pass: count remaining entries and evict oldest if over limit.
	type modelEntry struct {
		model string
		ts    int64
	}
	var entries []modelEntry
	stat.modelTTFTLastUpdate.Range(func(key, value any) bool {
		model, _ := key.(string)
		ts, _ := value.(int64)
		entries = append(entries, modelEntry{model: model, ts: ts})
		return true
	})

	if len(entries) <= maxModels {
		return
	}

	// Sort by timestamp ascending (oldest first) and evict surplus.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ts < entries[j].ts
	})
	evictCount := len(entries) - maxModels
	for i := 0; i < evictCount; i++ {
		stat.modelTTFT.Delete(entries[i].model)
		stat.modelTTFTLastUpdate.Delete(entries[i].model)
	}
}

func updateEWMAAtomic(target *atomic.Uint64, sample float64, alpha float64) {
	for {
		oldBits := target.Load()
		oldValue := math.Float64frombits(oldBits)
		newValue := alpha*sample + (1-alpha)*oldValue
		if target.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			return
		}
	}
}

func (s *openAIAccountRuntimeStats) report(accountID int64, success bool, firstTokenMs *int, model string, ttftMs float64) {
	s.reportWithOptions(
		accountID,
		success,
		firstTokenMs,
		defaultCircuitBreakerFailThreshold,
		true,
		model,
		ttftMs,
		true,
		defaultPerModelTTFTMaxModels,
	)
}

func (s *openAIAccountRuntimeStats) reportWithCB(accountID int64, success bool, firstTokenMs *int, cbThreshold int, model string, ttftMs float64) {
	s.reportWithOptions(
		accountID,
		success,
		firstTokenMs,
		cbThreshold,
		true,
		model,
		ttftMs,
		true,
		defaultPerModelTTFTMaxModels,
	)
}

func (s *openAIAccountRuntimeStats) reportWithOptions(
	accountID int64,
	success bool,
	firstTokenMs *int,
	cbThreshold int,
	updateCircuitBreaker bool,
	model string,
	ttftMs float64,
	perModelTTFTEnabled bool,
	perModelTTFTMaxModels int,
) {
	if s == nil || accountID <= 0 {
		return
	}
	stat := s.loadOrCreate(accountID)
	stat.lastReportNano.Store(time.Now().UnixNano())

	errorSample := 1.0
	if success {
		errorSample = 0.0
	}
	stat.errorRate.update(errorSample)

	// Per-model TTFT tracking: reportModelTTFT updates both per-model and
	// global TTFT, so skip the separate global update to avoid double-counting.
	if perModelTTFTEnabled && model != "" && ttftMs > 0 {
		stat.reportModelTTFT(model, ttftMs)
	} else if firstTokenMs != nil && *firstTokenMs > 0 {
		stat.ttft.update(float64(*firstTokenMs))
	}

	// Update circuit breaker state only when feature is enabled.
	if updateCircuitBreaker {
		cb := s.getCircuitBreaker(accountID)
		if success {
			cb.recordSuccess()
		} else {
			cb.recordFailure(cbThreshold)
		}
	}

	// Periodic cleanup: every 100 reports.
	cnt := s.cleanupCounter.Add(1)
	if cnt%100 == 0 {
		maxModels := defaultPerModelTTFTMaxModels
		if perModelTTFTMaxModels > 0 {
			maxModels = perModelTTFTMaxModels
		}
		stat.cleanupStaleTTFT(defaultPerModelTTFTTTL, maxModels)
	}
	// GC inactive accounts and orphaned circuit breakers: every 1000 reports.
	if cnt%1000 == 0 {
		s.gcInactiveAccounts(time.Hour)
	}
}

func (s *openAIAccountRuntimeStats) snapshot(accountID int64, model ...string) (errorRate float64, ttft float64, hasTTFT bool) {
	if s == nil || accountID <= 0 {
		return 0, 0, false
	}
	value, ok := s.accounts.Load(accountID)
	if !ok {
		return 0, 0, false
	}
	stat, _ := value.(*openAIAccountRuntimeStat)
	if stat == nil {
		return 0, 0, false
	}
	errorRate = clamp01(stat.errorRate.value())

	// Try per-model TTFT first; fallback to global.
	if len(model) > 0 && model[0] != "" {
		if mTTFT, mOK := stat.modelTTFTValue(model[0]); mOK {
			return errorRate, mTTFT, true
		}
	}

	ttft, hasTTFT = stat.ttft.value()
	return errorRate, ttft, hasTTFT
}

func (s *openAIAccountRuntimeStats) size() int {
	if s == nil {
		return 0
	}
	return int(s.accountCount.Load())
}

// gcInactiveAccounts removes account stats and circuit breakers that have not
// received any report for longer than maxIdle. This prevents unbounded growth
// of the sync.Maps when accounts are created and then deleted/deactivated.
func (s *openAIAccountRuntimeStats) gcInactiveAccounts(maxIdle time.Duration) {
	if s == nil {
		return
	}
	cutoff := time.Now().UnixNano() - int64(maxIdle)
	s.accounts.Range(func(key, value any) bool {
		stat, _ := value.(*openAIAccountRuntimeStat)
		if stat == nil || stat.lastReportNano.Load() < cutoff {
			s.accounts.Delete(key)
			s.circuitBreakers.Delete(key)
			s.accountCount.Add(-1)
		}
		return true
	})
}

type defaultOpenAIAccountScheduler struct {
	service *OpenAIGatewayService
	metrics openAIAccountSchedulerMetrics
	stats   *openAIAccountRuntimeStats
}

func newDefaultOpenAIAccountScheduler(service *OpenAIGatewayService, stats *openAIAccountRuntimeStats) OpenAIAccountScheduler {
	if stats == nil {
		stats = newOpenAIAccountRuntimeStats()
	}
	return &defaultOpenAIAccountScheduler{
		service: service,
		stats:   stats,
	}
}

func (s *defaultOpenAIAccountScheduler) Select(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{}
	start := time.Now()
	defer func() {
		decision.LatencyMs = time.Since(start).Milliseconds()
		s.metrics.recordSelect(decision)
	}()

	previousResponseID := strings.TrimSpace(req.PreviousResponseID)
	if previousResponseID != "" {
		selection, err := s.service.SelectAccountByPreviousResponseID(
			ctx,
			req.GroupID,
			previousResponseID,
			req.RequestedModel,
			req.ExcludedIDs,
		)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Account != nil {
			if !s.isAccountTransportCompatible(selection.Account, req.RequiredTransport) {
				selection = nil
			}
		}
		if selection != nil && selection.Account != nil {
			decision.Layer = openAIAccountScheduleLayerPreviousResponse
			decision.StickyPreviousHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			if req.SessionHash != "" {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
			}
			return selection, decision, nil
		}
	}

	selection, err := s.selectBySessionHash(ctx, req)
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		decision.Layer = openAIAccountScheduleLayerSessionSticky
		decision.StickySessionHit = true
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		return selection, decision, nil
	}

	selection, candidateCount, topK, loadSkew, err := s.selectByLoadBalance(ctx, req)
	decision.Layer = openAIAccountScheduleLayerLoadBalance
	decision.CandidateCount = candidateCount
	decision.TopK = topK
	decision.LoadSkew = loadSkew
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
	}
	return selection, decision, nil
}

func (s *defaultOpenAIAccountScheduler) selectBySessionHash(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, error) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || s.service == nil || s.service.cache == nil {
		return nil, nil
	}

	accountID := req.StickyAccountID
	if accountID <= 0 {
		var err error
		accountID, err = s.service.getStickySessionAccountID(ctx, req.GroupID, sessionHash)
		if err != nil || accountID <= 0 {
			return nil, nil
		}
	}
	if accountID <= 0 {
		return nil, nil
	}
	if req.ExcludedIDs != nil {
		if _, excluded := req.ExcludedIDs[accountID]; excluded {
			return nil, nil
		}
	}

	account, err := s.service.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, nil
	}
	if shouldClearStickySession(account, req.RequestedModel) || !account.IsOpenAI() {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, nil
	}
	if req.RequestedModel != "" && !account.IsModelSupported(req.RequestedModel) {
		return nil, nil
	}
	if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, nil
	}

	// Conditional sticky: release binding if account is unhealthy or overloaded.
	if s.shouldReleaseStickySession(accountID) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		return nil, nil // Fall through to load balance
	}

	result, acquireErr := s.service.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	if acquireErr == nil && result.Acquired {
		_ = s.service.refreshStickySessionTTL(ctx, req.GroupID, sessionHash, s.service.openAIWSSessionStickyTTL())
		return &AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}, nil
	}

	cfg := s.service.schedulingConfig()
	if s.service.concurrencyService != nil {
		waitingCount, _ := s.service.concurrencyService.GetAccountWaitingCount(ctx, accountID)
		if waitingCount < cfg.StickySessionMaxWaiting {
			return &AccountSelectionResult{
				Account: account,
				WaitPlan: &AccountWaitPlan{
					AccountID:      accountID,
					MaxConcurrency: account.Concurrency,
					Timeout:        cfg.StickySessionWaitTimeout,
					MaxWaiting:     cfg.StickySessionMaxWaiting,
				},
			}, nil
		}
	}
	return nil, nil
}

type openAIAccountCandidateScore struct {
	account   *Account
	loadInfo  *AccountLoadInfo
	score     float64
	errorRate float64
	ttft      float64
	hasTTFT   bool
}

type openAIAccountCandidateHeap []openAIAccountCandidateScore

func (h openAIAccountCandidateHeap) Len() int {
	return len(h)
}

func (h openAIAccountCandidateHeap) Less(i, j int) bool {
	// 最小堆根节点保存“最差”候选，便于 O(log k) 维护 topK。
	return isOpenAIAccountCandidateBetter(h[j], h[i])
}

func (h openAIAccountCandidateHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *openAIAccountCandidateHeap) Push(x any) {
	*h = append(*h, x.(openAIAccountCandidateScore))
}

func (h *openAIAccountCandidateHeap) Pop() any {
	old := *h
	n := len(old)
	last := old[n-1]
	*h = old[:n-1]
	return last
}

func isOpenAIAccountCandidateBetter(left openAIAccountCandidateScore, right openAIAccountCandidateScore) bool {
	if left.score != right.score {
		return left.score > right.score
	}
	if left.account.Priority != right.account.Priority {
		return left.account.Priority < right.account.Priority
	}
	if left.loadInfo.LoadRate != right.loadInfo.LoadRate {
		return left.loadInfo.LoadRate < right.loadInfo.LoadRate
	}
	if left.loadInfo.WaitingCount != right.loadInfo.WaitingCount {
		return left.loadInfo.WaitingCount < right.loadInfo.WaitingCount
	}
	return left.account.ID < right.account.ID
}

func selectTopKOpenAICandidates(candidates []openAIAccountCandidateScore, topK int) []openAIAccountCandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = 1
	}
	if topK >= len(candidates) {
		ranked := append([]openAIAccountCandidateScore(nil), candidates...)
		sort.Slice(ranked, func(i, j int) bool {
			return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
		})
		return ranked
	}

	best := make(openAIAccountCandidateHeap, 0, topK)
	for _, candidate := range candidates {
		if len(best) < topK {
			heap.Push(&best, candidate)
			continue
		}
		if isOpenAIAccountCandidateBetter(candidate, best[0]) {
			best[0] = candidate
			heap.Fix(&best, 0)
		}
	}

	ranked := make([]openAIAccountCandidateScore, len(best))
	copy(ranked, best)
	sort.Slice(ranked, func(i, j int) bool {
		return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
	})
	return ranked
}

type openAISelectionRNG struct {
	state uint64
}

func newOpenAISelectionRNG(seed uint64) openAISelectionRNG {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	return openAISelectionRNG{state: seed}
}

func (r *openAISelectionRNG) nextUint64() uint64 {
	// xorshift64*
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 2685821657736338717
}

func (r *openAISelectionRNG) nextFloat64() float64 {
	// [0,1)
	return float64(r.nextUint64()>>11) / (1 << 53)
}

func deriveOpenAISelectionSeed(req OpenAIAccountScheduleRequest) uint64 {
	hasher := fnv.New64a()
	writeValue := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		_, _ = hasher.Write([]byte(trimmed))
		_, _ = hasher.Write([]byte{0})
	}

	writeValue(req.SessionHash)
	writeValue(req.PreviousResponseID)
	writeValue(req.RequestedModel)
	if req.GroupID != nil {
		_, _ = hasher.Write([]byte(strconv.FormatInt(*req.GroupID, 10)))
	}

	seed := hasher.Sum64()
	// 对“无会话锚点”的纯负载均衡请求引入时间熵，避免固定命中同一账号。
	if strings.TrimSpace(req.SessionHash) == "" && strings.TrimSpace(req.PreviousResponseID) == "" {
		seed ^= uint64(time.Now().UnixNano())
	}
	if seed == 0 {
		seed = uint64(time.Now().UnixNano()) ^ 0x9e3779b97f4a7c15
	}
	return seed
}

func buildOpenAIWeightedSelectionOrder(
	candidates []openAIAccountCandidateScore,
	req OpenAIAccountScheduleRequest,
) []openAIAccountCandidateScore {
	if len(candidates) <= 1 {
		return append([]openAIAccountCandidateScore(nil), candidates...)
	}

	pool := append([]openAIAccountCandidateScore(nil), candidates...)
	weights := make([]float64, len(pool))
	minScore := pool[0].score
	for i := 1; i < len(pool); i++ {
		if pool[i].score < minScore {
			minScore = pool[i].score
		}
	}
	for i := range pool {
		// 将 top-K 分值平移到正区间，避免“单一最高分账号”长期垄断。
		weight := (pool[i].score - minScore) + 1.0
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			weight = 1.0
		}
		weights[i] = weight
	}

	order := make([]openAIAccountCandidateScore, 0, len(pool))
	rng := newOpenAISelectionRNG(deriveOpenAISelectionSeed(req))
	for len(pool) > 0 {
		total := 0.0
		for _, w := range weights {
			total += w
		}

		selectedIdx := 0
		if total > 0 {
			r := rng.nextFloat64() * total
			acc := 0.0
			for i, w := range weights {
				acc += w
				if r <= acc {
					selectedIdx = i
					break
				}
			}
		} else {
			selectedIdx = int(rng.nextUint64() % uint64(len(pool)))
		}

		order = append(order, pool[selectedIdx])
		pool = append(pool[:selectedIdx], pool[selectedIdx+1:]...)
		weights = append(weights[:selectedIdx], weights[selectedIdx+1:]...)
	}
	return order
}

// selectP2COpenAICandidates selects candidates using Power-of-Two-Choices:
// randomly pick 2 candidates, return the one with the higher score.
// Repeat to build a full selection order for fallback.
func selectP2COpenAICandidates(
	candidates []openAIAccountCandidateScore,
	req OpenAIAccountScheduleRequest,
) []openAIAccountCandidateScore {
	if len(candidates) <= 1 {
		return append([]openAIAccountCandidateScore(nil), candidates...)
	}

	rng := newOpenAISelectionRNG(deriveOpenAISelectionSeed(req))
	pool := append([]openAIAccountCandidateScore(nil), candidates...)
	order := make([]openAIAccountCandidateScore, 0, len(pool))

	for len(pool) > 1 {
		n := uint64(len(pool))
		// Pick first random index.
		idx1 := int(rng.nextUint64() % n)
		// Pick second random index, distinct from the first.
		idx2 := int(rng.nextUint64() % (n - 1))
		if idx2 >= idx1 {
			idx2++
		}

		// Compare: take the candidate with the higher score.
		winner := idx1
		if isOpenAIAccountCandidateBetter(pool[idx2], pool[idx1]) {
			winner = idx2
		}

		order = append(order, pool[winner])
		// Remove winner from pool (swap with last element for O(1) removal).
		pool[winner] = pool[len(pool)-1]
		pool = pool[:len(pool)-1]
	}
	// Append the last remaining candidate.
	order = append(order, pool[0])
	return order
}

// ---------------------------------------------------------------------------
// Softmax Temperature Sampling
// ---------------------------------------------------------------------------

const defaultSoftmaxTemperature = 0.3

type softmaxConfig struct {
	enabled     bool
	temperature float64
}

// softmaxConfigRead reads softmax scheduler config with fallback defaults.
func (s *defaultOpenAIAccountScheduler) softmaxConfigRead() softmaxConfig {
	if s == nil || s.service == nil || s.service.cfg == nil {
		return softmaxConfig{}
	}
	wsCfg := s.service.cfg.Gateway.OpenAIWS
	temp := wsCfg.SchedulerSoftmaxTemperature
	if temp <= 0 {
		temp = defaultSoftmaxTemperature
	}
	return softmaxConfig{
		enabled:     wsCfg.SchedulerSoftmaxEnabled,
		temperature: temp,
	}
}

// selectSoftmaxOpenAICandidates applies softmax temperature sampling to select
// one candidate probabilistically, then returns the full list with the selected
// candidate first and the rest sorted by descending probability.
//
// Algorithm (numerically stable):
//
//	maxScore = max(score[i])
//	weights[i] = exp((score[i] - maxScore) / temperature)
//	probability[i] = weights[i] / sum(weights)
//
// A higher temperature yields more uniform selection (exploration); a lower
// temperature concentrates probability on the highest-scored candidates
// (exploitation).
func selectSoftmaxOpenAICandidates(
	candidates []openAIAccountCandidateScore,
	temperature float64,
	rng *openAISelectionRNG,
) []openAIAccountCandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return append([]openAIAccountCandidateScore(nil), candidates...)
	}
	if temperature <= 0 {
		temperature = defaultSoftmaxTemperature
	}

	// Step 1: find max score for numerical stability.
	maxScore := candidates[0].score
	for i := 1; i < len(candidates); i++ {
		if candidates[i].score > maxScore {
			maxScore = candidates[i].score
		}
	}

	// Step 2: compute softmax weights.
	type indexedProb struct {
		index int
		prob  float64
	}
	probs := make([]indexedProb, len(candidates))
	sumWeights := 0.0
	for i := range candidates {
		w := math.Exp((candidates[i].score - maxScore) / temperature)
		// Guard against NaN/Inf from degenerate inputs.
		if math.IsNaN(w) || math.IsInf(w, 0) {
			w = 0
		}
		probs[i] = indexedProb{index: i, prob: w}
		sumWeights += w
	}

	// Normalise to probabilities. If sumWeights is zero (all weights collapsed
	// to zero, which can happen with extreme negative scores), fall back to
	// uniform distribution.
	if sumWeights > 0 {
		for i := range probs {
			probs[i].prob /= sumWeights
		}
	} else {
		uniform := 1.0 / float64(len(probs))
		for i := range probs {
			probs[i].prob = uniform
		}
	}

	// Step 3: sample ONE candidate via CDF.
	r := rng.nextFloat64()
	selectedIdx := probs[len(probs)-1].index // default to last if rounding issues
	cumulative := 0.0
	for _, ip := range probs {
		cumulative += ip.prob
		if cumulative >= r {
			selectedIdx = ip.index
			break
		}
	}

	// Step 4: build result — selected candidate first, rest sorted by
	// descending probability.
	result := make([]openAIAccountCandidateScore, 0, len(candidates))
	result = append(result, candidates[selectedIdx])

	// Sort remaining by probability descending for fallback order.
	remaining := make([]indexedProb, 0, len(probs)-1)
	for _, ip := range probs {
		if ip.index != selectedIdx {
			remaining = append(remaining, ip)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].prob > remaining[j].prob
	})
	for _, ip := range remaining {
		result = append(result, candidates[ip.index])
	}

	return result
}

func (s *defaultOpenAIAccountScheduler) selectByLoadBalance(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, int, int, float64, error) {
	accounts, err := s.service.listSchedulableAccounts(ctx, req.GroupID)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	if len(accounts) == 0 {
		return nil, 0, 0, 0, errors.New("no available OpenAI accounts")
	}

	filtered := make([]*Account, 0, len(accounts))
	loadReq := make([]AccountWithConcurrency, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[account.ID]; excluded {
				continue
			}
		}
		if !account.IsSchedulable() || !account.IsOpenAI() {
			continue
		}
		if req.RequestedModel != "" && !account.IsModelSupported(req.RequestedModel) {
			continue
		}
		if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
			continue
		}
		filtered = append(filtered, account)
		loadReq = append(loadReq, AccountWithConcurrency{
			ID:             account.ID,
			MaxConcurrency: account.Concurrency,
		})
	}
	if len(filtered) == 0 {
		return nil, 0, 0, 0, errors.New("no available OpenAI accounts")
	}

	// Circuit breaker filtering: remove accounts with open CBs, but if that
	// would empty the candidate pool, keep all accounts (graceful degradation).
	cbEnabled, _, cbCooldown, cbHalfOpenMax := s.schedulerCircuitBreakerConfig()
	heldHalfOpenPermits := make(map[int64]*accountCircuitBreaker)
	releaseHalfOpenPermit := func(accountID int64) {
		cb, ok := heldHalfOpenPermits[accountID]
		if !ok || cb == nil {
			return
		}
		cb.releaseHalfOpenPermit()
		delete(heldHalfOpenPermits, accountID)
	}
	defer func() {
		for accountID := range heldHalfOpenPermits {
			releaseHalfOpenPermit(accountID)
		}
	}()
	if cbEnabled {
		healthy := make([]*Account, 0, len(filtered))
		healthyLoadReq := make([]AccountWithConcurrency, 0, len(loadReq))
		for i, account := range filtered {
			cb := s.stats.loadCircuitBreaker(account.ID)
			if cb == nil || cb.allow(cbCooldown, cbHalfOpenMax) {
				healthy = append(healthy, account)
				healthyLoadReq = append(healthyLoadReq, loadReq[i])
				if cb.isHalfOpen() {
					heldHalfOpenPermits[account.ID] = cb
				}
			}
		}
		if len(healthy) > 0 {
			filtered = healthy
			loadReq = healthyLoadReq
		}
		// else: all accounts are circuit-open; fall through with the
		// original set to avoid returning "no accounts".
	}

	loadMap := map[int64]*AccountLoadInfo{}
	if s.service.concurrencyService != nil {
		if batchLoad, loadErr := s.service.concurrencyService.GetAccountsLoadBatch(ctx, loadReq); loadErr == nil {
			loadMap = batchLoad
		}
	}

	trendEnabled, trendMaxSlope := s.service.openAIWSSchedulerTrendConfig()
	perModelTTFTEnabled, _ := s.schedulerPerModelTTFTConfig()
	requestedModelForStats := ""
	if perModelTTFTEnabled {
		requestedModelForStats = req.RequestedModel
	}

	minPriority, maxPriority := filtered[0].Priority, filtered[0].Priority
	maxWaiting := 1
	maxConcurrency := 0
	loadRateSum := 0.0
	loadRateSumSquares := 0.0
	minTTFT, maxTTFT := 0.0, 0.0
	hasTTFTSample := false
	candidates := make([]openAIAccountCandidateScore, 0, len(filtered))
	for _, account := range filtered {
		loadInfo := loadMap[account.ID]
		if loadInfo == nil {
			loadInfo = &AccountLoadInfo{AccountID: account.ID}
		}
		if account.Priority < minPriority {
			minPriority = account.Priority
		}
		if account.Priority > maxPriority {
			maxPriority = account.Priority
		}
		if loadInfo.WaitingCount > maxWaiting {
			maxWaiting = loadInfo.WaitingCount
		}
		if account.Concurrency > maxConcurrency {
			maxConcurrency = account.Concurrency
		}
		errorRate, ttft, hasTTFT := s.stats.snapshot(account.ID, requestedModelForStats)
		if hasTTFT && ttft > 0 {
			if !hasTTFTSample {
				minTTFT, maxTTFT = ttft, ttft
				hasTTFTSample = true
			} else {
				if ttft < minTTFT {
					minTTFT = ttft
				}
				if ttft > maxTTFT {
					maxTTFT = ttft
				}
			}
		}
		loadRate := float64(loadInfo.LoadRate)
		loadRateSum += loadRate
		loadRateSumSquares += loadRate * loadRate

		// Record current load rate sample for trend tracking.
		if trendEnabled {
			stat := s.stats.loadOrCreate(account.ID)
			stat.loadTrend.record(loadRate)
		}

		candidates = append(candidates, openAIAccountCandidateScore{
			account:   account,
			loadInfo:  loadInfo,
			errorRate: errorRate,
			ttft:      ttft,
			hasTTFT:   hasTTFT,
		})
	}
	loadSkew := calcLoadSkewByMoments(loadRateSum, loadRateSumSquares, len(candidates))

	weights := s.service.openAIWSSchedulerWeights()
	for i := range candidates {
		item := &candidates[i]
		priorityFactor := 1.0
		if maxPriority > minPriority {
			priorityFactor = 1 - float64(item.account.Priority-minPriority)/float64(maxPriority-minPriority)
		}
		// Base load factor from percentage utilization.
		loadFactor := 1 - clamp01(float64(item.loadInfo.LoadRate)/100.0)
		// Capacity-aware adjustment: accounts with more absolute headroom get a bonus.
		if maxConcurrency > 0 && item.account.Concurrency > 0 {
			remainingSlots := float64(item.account.Concurrency) * (1 - float64(item.loadInfo.LoadRate)/100.0)
			capacityBonus := clamp01(remainingSlots / float64(maxConcurrency))
			// Blend: 70% relative load + 30% capacity bonus
			loadFactor = 0.7*loadFactor + 0.3*capacityBonus
		}

		// Trend adjustment: penalise accounts whose load is rising, reward those declining.
		// trendAdj ranges [0, 1] where 0 = max rising slope, 1 = max falling/flat slope.
		// loadFactor is blended: 70% base load + 30% trend influence.
		if trendEnabled {
			stat := s.stats.loadOrCreate(item.account.ID)
			slope := stat.loadTrend.slope()
			trendAdj := 1.0 - clamp01(slope/trendMaxSlope)
			loadFactor *= (0.7 + 0.3*trendAdj)
		}

		queueFactor := 1 - clamp01(float64(item.loadInfo.WaitingCount)/float64(maxWaiting))
		// Queue depth relative to account's own capacity for capacity-aware blending.
		if item.account.Concurrency > 0 {
			relativeQueue := clamp01(float64(item.loadInfo.WaitingCount) / float64(item.account.Concurrency))
			// Blend: 60% cross-account normalized + 40% self-relative
			queueFactor = 0.6*queueFactor + 0.4*(1-relativeQueue)
		}
		errorFactor := 1 - clamp01(item.errorRate)
		ttftFactor := 0.5
		if item.hasTTFT && hasTTFTSample && maxTTFT > minTTFT {
			ttftFactor = 1 - clamp01((item.ttft-minTTFT)/(maxTTFT-minTTFT))
		}

		item.score = weights.Priority*priorityFactor +
			weights.Load*loadFactor +
			weights.Queue*queueFactor +
			weights.ErrorRate*errorFactor +
			weights.TTFT*ttftFactor
	}

	var selectionOrder []openAIAccountCandidateScore
	topK := 0
	rng := newOpenAISelectionRNG(deriveOpenAISelectionSeed(req))
	smCfg := s.softmaxConfigRead()
	p2cEnabled := s.service.openAIWSSchedulerP2CEnabled()
	if smCfg.enabled && len(candidates) > 3 {
		selectionOrder = selectSoftmaxOpenAICandidates(candidates, smCfg.temperature, &rng)
		// topK = 0 signals softmax mode in metrics / decision struct.
	} else if p2cEnabled {
		selectionOrder = selectP2COpenAICandidates(candidates, req)
		// topK = 0 signals P2C mode in metrics / decision struct.
	} else {
		topK = s.service.openAIWSLBTopK()
		if topK > len(candidates) {
			topK = len(candidates)
		}
		if topK <= 0 {
			topK = 1
		}
		rankedCandidates := selectTopKOpenAICandidates(candidates, topK)
		selectionOrder = buildOpenAIWeightedSelectionOrder(rankedCandidates, req)
	}

	for i := 0; i < topK; i++ {
		candidate := rankedCandidates[i]
		result, acquireErr := s.service.tryAcquireAccountSlot(ctx, candidate.account.ID, candidate.account.Concurrency)
		if acquireErr != nil {
			releaseHalfOpenPermit(candidate.account.ID)
			return nil, len(candidates), topK, loadSkew, acquireErr
		}
		if result != nil && result.Acquired {
			// Keep HALF_OPEN permit for the selected account; the outcome will be
			// settled by ReportResult(success/failure) after the request finishes.
			delete(heldHalfOpenPermits, candidate.account.ID)
			if req.SessionHash != "" {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, candidate.account.ID)
			}
			return &AccountSelectionResult{
				Account:     candidate.account,
				Acquired:    true,
				ReleaseFunc: result.ReleaseFunc,
			}, len(candidates), topK, loadSkew, nil
		}
		releaseHalfOpenPermit(candidate.account.ID)
	}

	cfg := s.service.schedulingConfig()
	candidate := selectionOrder[0]
	releaseHalfOpenPermit(candidate.account.ID)
	return &AccountSelectionResult{
		Account: candidate.account,
		WaitPlan: &AccountWaitPlan{
			AccountID:      candidate.account.ID,
			MaxConcurrency: candidate.account.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		},
	}, len(candidates), topK, loadSkew, nil
}

func (s *defaultOpenAIAccountScheduler) isAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	// HTTP 入站可回退到 HTTP 线路，不需要在账号选择阶段做传输协议强过滤。
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || s.service == nil || account == nil {
		return false
	}
	return s.service.getOpenAIWSProtocolResolver().Resolve(account).Transport == requiredTransport
}

func (s *defaultOpenAIAccountScheduler) ReportResult(accountID int64, success bool, firstTokenMs *int, model string, ttftMs float64) {
	if s == nil || s.stats == nil {
		return
	}
	perModelTTFTEnabled, perModelTTFTMaxModels := s.schedulerPerModelTTFTConfig()
	enabled, threshold, _, _ := s.schedulerCircuitBreakerConfig()
	if !enabled {
		// Circuit breaker disabled: only update runtime signals (error-rate/TTFT),
		// do not mutate circuit breaker state.
		s.stats.reportWithOptions(
			accountID,
			success,
			firstTokenMs,
			0,
			false,
			model,
			ttftMs,
			perModelTTFTEnabled,
			perModelTTFTMaxModels,
		)
		return
	}

	// Snapshot state before the update for metrics tracking.
	cb := s.stats.getCircuitBreaker(accountID)
	stateBefore := cb.state.Load()

	s.stats.reportWithOptions(
		accountID,
		success,
		firstTokenMs,
		threshold,
		true,
		model,
		ttftMs,
		perModelTTFTEnabled,
		perModelTTFTMaxModels,
	)

	stateAfter := cb.state.Load()
	// CLOSED/HALF_OPEN → OPEN: circuit tripped.
	if stateBefore != circuitBreakerStateOpen && stateAfter == circuitBreakerStateOpen {
		s.metrics.circuitBreakerOpenTotal.Add(1)
	}
	// OPEN/HALF_OPEN → CLOSED: circuit recovered.
	if stateBefore != circuitBreakerStateClosed && stateAfter == circuitBreakerStateClosed {
		s.metrics.circuitBreakerRecoverTotal.Add(1)
	}
}

func (s *defaultOpenAIAccountScheduler) ReportSwitch() {
	if s == nil {
		return
	}
	s.metrics.recordSwitch()
}

// schedulerCircuitBreakerConfig reads CB config with fallback defaults.
func (s *defaultOpenAIAccountScheduler) schedulerCircuitBreakerConfig() (enabled bool, threshold int, cooldown time.Duration, halfOpenMax int) {
	threshold = defaultCircuitBreakerFailThreshold
	cooldown = time.Duration(defaultCircuitBreakerCooldownSec) * time.Second
	halfOpenMax = defaultCircuitBreakerHalfOpenMax

	if s == nil || s.service == nil || s.service.cfg == nil {
		return false, threshold, cooldown, halfOpenMax
	}
	wsCfg := s.service.cfg.Gateway.OpenAIWS
	enabled = wsCfg.SchedulerCircuitBreakerEnabled
	if wsCfg.SchedulerCircuitBreakerFailThreshold > 0 {
		threshold = wsCfg.SchedulerCircuitBreakerFailThreshold
	}
	if wsCfg.SchedulerCircuitBreakerCooldownSec > 0 {
		cooldown = time.Duration(wsCfg.SchedulerCircuitBreakerCooldownSec) * time.Second
	}
	if wsCfg.SchedulerCircuitBreakerHalfOpenMax > 0 {
		halfOpenMax = wsCfg.SchedulerCircuitBreakerHalfOpenMax
	}
	return enabled, threshold, cooldown, halfOpenMax
}

func (s *defaultOpenAIAccountScheduler) schedulerPerModelTTFTConfig() (enabled bool, maxModels int) {
	maxModels = defaultPerModelTTFTMaxModels
	if s == nil || s.service == nil || s.service.cfg == nil {
		return false, maxModels
	}
	wsCfg := s.service.cfg.Gateway.OpenAIWS
	enabled = wsCfg.SchedulerPerModelTTFTEnabled
	if wsCfg.SchedulerPerModelTTFTMaxModels > 0 {
		maxModels = wsCfg.SchedulerPerModelTTFTMaxModels
	}
	return enabled, maxModels
}

// ---------------------------------------------------------------------------
// Conditional Sticky Session Release
// ---------------------------------------------------------------------------

const defaultStickyReleaseErrorThreshold = 0.3

type stickyReleaseConfig struct {
	enabled        bool
	errorThreshold float64
}

// stickyReleaseConfigRead reads conditional sticky release config with defaults.
func (s *defaultOpenAIAccountScheduler) stickyReleaseConfigRead() stickyReleaseConfig {
	if s == nil || s.service == nil || s.service.cfg == nil {
		return stickyReleaseConfig{}
	}
	wsCfg := s.service.cfg.Gateway.OpenAIWS
	threshold := wsCfg.StickyReleaseErrorThreshold
	if threshold <= 0 {
		threshold = defaultStickyReleaseErrorThreshold
	}
	return stickyReleaseConfig{
		enabled:        wsCfg.StickyReleaseEnabled,
		errorThreshold: threshold,
	}
}

// shouldReleaseStickySession checks whether a sticky binding should be
// released because the account is unhealthy (circuit breaker open) or has a
// high error rate. This runs BEFORE slot acquisition to avoid wasting
// concurrency capacity on degraded accounts.
func (s *defaultOpenAIAccountScheduler) shouldReleaseStickySession(accountID int64) bool {
	if s == nil || s.stats == nil || s.service == nil {
		return false
	}

	cfg := s.stickyReleaseConfigRead()
	if !cfg.enabled {
		return false
	}

	// Check 1: Circuit breaker is open -> immediate release.
	// Only check if CB feature is actually enabled, because the default CB
	// threshold (5) is very aggressive and may trip unexpectedly.
	cbEnabled, _, _, _ := s.schedulerCircuitBreakerConfig()
	if cbEnabled && s.stats.isCircuitOpen(accountID) {
		s.metrics.stickyReleaseCircuitOpenTotal.Add(1)
		return true
	}

	// Check 2: Error rate exceeds threshold -> immediate release.
	// Guard against cold-start: the EWMA error rate is unreliable when
	// fewer than dualEWMAMinSamples have been collected.
	stat := s.stats.loadExisting(accountID)
	if stat != nil && stat.errorRate.isWarmedUp() {
		errorRate, _, _ := s.stats.snapshot(accountID)
		if errorRate > cfg.errorThreshold {
			s.metrics.stickyReleaseErrorTotal.Add(1)
			return true
		}
	}

	return false
}

func (s *defaultOpenAIAccountScheduler) SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	if s == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}

	selectTotal := s.metrics.selectTotal.Load()
	prevHit := s.metrics.stickyPreviousHitTotal.Load()
	sessionHit := s.metrics.stickySessionHitTotal.Load()
	switchTotal := s.metrics.accountSwitchTotal.Load()
	latencyTotal := s.metrics.latencyMsTotal.Load()
	loadSkewTotal := s.metrics.loadSkewMilliTotal.Load()

	snapshot := OpenAIAccountSchedulerMetricsSnapshot{
		SelectTotal:                   selectTotal,
		StickyPreviousHitTotal:        prevHit,
		StickySessionHitTotal:         sessionHit,
		LoadBalanceSelectTotal:        s.metrics.loadBalanceSelectTotal.Load(),
		AccountSwitchTotal:            switchTotal,
		SchedulerLatencyMsTotal:       latencyTotal,
		RuntimeStatsAccountCount:      s.stats.size(),
		CircuitBreakerOpenTotal:       s.metrics.circuitBreakerOpenTotal.Load(),
		CircuitBreakerRecoverTotal:    s.metrics.circuitBreakerRecoverTotal.Load(),
		StickyReleaseErrorTotal:       s.metrics.stickyReleaseErrorTotal.Load(),
		StickyReleaseCircuitOpenTotal: s.metrics.stickyReleaseCircuitOpenTotal.Load(),
	}
	if selectTotal > 0 {
		snapshot.SchedulerLatencyMsAvg = float64(latencyTotal) / float64(selectTotal)
		snapshot.StickyHitRatio = float64(prevHit+sessionHit) / float64(selectTotal)
		snapshot.AccountSwitchRate = float64(switchTotal) / float64(selectTotal)
		snapshot.LoadSkewAvg = float64(loadSkewTotal) / 1000 / float64(selectTotal)
	}
	return snapshot
}

func (s *OpenAIGatewayService) getOpenAIAccountScheduler() OpenAIAccountScheduler {
	if s == nil {
		return nil
	}
	s.openaiSchedulerOnce.Do(func() {
		if s.openaiAccountStats == nil {
			s.openaiAccountStats = newOpenAIAccountRuntimeStats()
		}
		if s.openaiScheduler == nil {
			s.openaiScheduler = newDefaultOpenAIAccountScheduler(s, s.openaiAccountStats)
		}
	})
	return s.openaiScheduler
}

func (s *OpenAIGatewayService) SelectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{}
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		selection, err := s.SelectAccountWithLoadAwareness(ctx, groupID, sessionHash, requestedModel, excludedIDs)
		decision.Layer = openAIAccountScheduleLayerLoadBalance
		return selection, decision, err
	}

	var stickyAccountID int64
	if sessionHash != "" && s.cache != nil {
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil && accountID > 0 {
			stickyAccountID = accountID
		}
	}

	return scheduler.Select(ctx, OpenAIAccountScheduleRequest{
		GroupID:            groupID,
		SessionHash:        sessionHash,
		StickyAccountID:    stickyAccountID,
		PreviousResponseID: previousResponseID,
		RequestedModel:     requestedModel,
		RequiredTransport:  requiredTransport,
		ExcludedIDs:        excludedIDs,
	})
}

func (s *OpenAIGatewayService) ReportOpenAIAccountScheduleResult(accountID int64, success bool, firstTokenMs *int, model string, ttftMs float64) {
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		return
	}
	scheduler.ReportResult(accountID, success, firstTokenMs, model, ttftMs)
}

func (s *OpenAIGatewayService) RecordOpenAIAccountSwitch() {
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		return
	}
	scheduler.ReportSwitch()
}

func (s *OpenAIGatewayService) SnapshotOpenAIAccountSchedulerMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	scheduler := s.getOpenAIAccountScheduler()
	if scheduler == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}
	return scheduler.SnapshotMetrics()
}

func (s *OpenAIGatewayService) openAIWSSessionStickyTTL() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
	}
	return openaiStickySessionTTL
}

func (s *OpenAIGatewayService) openAIWSLBTopK() int {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.LBTopK > 0 {
		return s.cfg.Gateway.OpenAIWS.LBTopK
	}
	return 3
}

func (s *OpenAIGatewayService) openAIWSSchedulerP2CEnabled() bool {
	if s != nil && s.cfg != nil {
		return s.cfg.Gateway.OpenAIWS.SchedulerP2CEnabled
	}
	return false
}

func (s *OpenAIGatewayService) openAIWSSchedulerWeights() GatewayOpenAIWSSchedulerScoreWeightsView {
	if s != nil && s.cfg != nil {
		return GatewayOpenAIWSSchedulerScoreWeightsView{
			Priority:  s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority,
			Load:      s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load,
			Queue:     s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue,
			ErrorRate: s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate,
			TTFT:      s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT,
		}
	}
	return GatewayOpenAIWSSchedulerScoreWeightsView{
		Priority:  1.0,
		Load:      1.0,
		Queue:     0.7,
		ErrorRate: 0.8,
		TTFT:      0.5,
	}
}

type GatewayOpenAIWSSchedulerScoreWeightsView struct {
	Priority  float64
	Load      float64
	Queue     float64
	ErrorRate float64
	TTFT      float64
}

// defaultSchedulerTrendMaxSlope is the normalization ceiling for the trend
// slope. A slope of 5.0 means the account's load rate is increasing at 5
// percentage points per second — a very steep rise.
const defaultSchedulerTrendMaxSlope = 5.0

// openAIWSSchedulerTrendConfig reads trend-prediction config with defaults.
func (s *OpenAIGatewayService) openAIWSSchedulerTrendConfig() (enabled bool, maxSlope float64) {
	maxSlope = defaultSchedulerTrendMaxSlope
	if s == nil || s.cfg == nil {
		return false, maxSlope
	}
	enabled = s.cfg.Gateway.OpenAIWS.SchedulerTrendEnabled
	if s.cfg.Gateway.OpenAIWS.SchedulerTrendMaxSlope > 0 {
		maxSlope = s.cfg.Gateway.OpenAIWS.SchedulerTrendMaxSlope
	}
	return enabled, maxSlope
}

func clamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func calcLoadSkew(loadRates []float64) float64 {
	sum := 0.0
	sumSquares := 0.0
	for _, value := range loadRates {
		sum += value
		sumSquares += value * value
	}
	return calcLoadSkewByMoments(sum, sumSquares, len(loadRates))
}

func calcLoadSkewByMoments(sum float64, sumSquares float64, count int) float64 {
	if count <= 1 {
		return 0
	}
	mean := sum / float64(count)
	variance := sumSquares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
