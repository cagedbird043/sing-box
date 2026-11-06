// Package nodeprobe screens group members by downloading a little real data
// through each of them and keeps every probe group on a member that passes.
package nodeprobe

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	boxService "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"
)

const (
	defaultDownloadBytes = 128 * 1024
	defaultTimeout       = 5 * time.Second
	// Healthy nodes measured 0.1–0.8 off-peak; 128 KB at 0.14 MB/s takes about 0.9 s,
	// over 1.5 times any first byte under 0.6 s.
	defaultMaxRTTRatio    = 1.5
	defaultScreenInterval = 30 * time.Minute
	defaultCheckInterval  = 5 * time.Minute

	screenConcurrency = 8
	// A failing connection triggers a check of its group at most this often.
	passiveCooldown = 15 * time.Second
	// A check triggered by a failing connection gives the selection this long.
	triggeredTimeout = 2 * time.Second
	// Interface events arrive in bursts; screen once they settle.
	networkSettle = 2 * time.Second
	// The domestic verdict is reused for this long within one burst of work.
	domesticCacheTime = 30 * time.Second
)

func RegisterService(registry *boxService.Registry) {
	boxService.Register[option.NodeProbeServiceOptions](registry, C.TypeNodeProbe, NewService)
}

// Group is a probe group as seen by the service.
type Group interface {
	Tag() string
	// Candidates returns the current members in configured order.
	Candidates() []adapter.Outbound
	// Current returns the selected member, or nil.
	Current() adapter.Outbound
	// Choose selects a member; reason is logged.
	Choose(outbound adapter.Outbound, reason string)
}

type GroupOptions struct {
	// URL replaces the download test with a plain request: any HTTP response passes.
	URL string
	// PreferOrder selects the first passing member in configured order instead of the lowest latency.
	PreferOrder bool
}

type Handle struct {
	service   *Service
	group     Group
	options   GroupOptions
	pending   atomic.Bool
	lastCheck time.Time
	// Request results of a URL group, guarded by service.reportsAccess.
	reports map[string]Report
	error   string
}

type result struct {
	ok    bool
	score time.Duration
}

// Report is the latest measurement of one member, readable from any goroutine.
type Report struct {
	Time      time.Time
	FirstByte time.Duration
	// Transfer is zero for a plain request.
	Transfer time.Duration
	OK       bool
	Error    string
	Ratio    float64
}

// State describes a complete shared screening, including skipped attempts.
type State struct {
	ScreenedAt time.Time
	Running    bool
	Generation uint64
	Error      string
}

type Service struct {
	boxService.Adapter
	logger         log.ContextLogger
	targets        []string
	downloadBytes  uint32
	timeout        time.Duration
	maxRatio       float64
	screenInterval time.Duration
	checkInterval  time.Duration
	domesticURL    string
	direct         N.Dialer
	pause          pause.Manager

	groupsAccess sync.Mutex
	groups       []*Handle

	networkChanged chan struct{}
	triggers       chan *Handle
	screenRequests chan struct{}
	history        *urltest.HistoryStorage

	reportsAccess sync.RWMutex
	reports       map[string]Report
	state         State
	screenPending bool

	// Owned by the loop goroutine.
	results      map[string]result
	round        int
	lastScreen   time.Time
	domesticAt   time.Time
	domesticPass bool
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.NodeProbeServiceOptions) (adapter.Service, error) {
	if len(options.Targets) == 0 {
		return nil, E.New("missing targets")
	}
	if options.DomesticURL == "" {
		return nil, E.New("missing domestic_url")
	}
	direct, err := dialer.New(ctx, option.DialerOptions{}, true)
	if err != nil {
		return nil, err
	}
	s := &Service{
		Adapter:        boxService.NewAdapter(C.TypeNodeProbe, tag),
		logger:         logger,
		targets:        options.Targets,
		downloadBytes:  options.DownloadBytes,
		timeout:        time.Duration(options.Timeout),
		maxRatio:       options.MaxRTTRatio,
		screenInterval: time.Duration(options.ScreenInterval),
		checkInterval:  time.Duration(options.CheckInterval),
		domesticURL:    options.DomesticURL,
		direct:         direct,
		pause:          service.FromContext[pause.Manager](ctx),
		networkChanged: make(chan struct{}, 1),
		triggers:       make(chan *Handle, 64),
		screenRequests: make(chan struct{}, 1),
		history:        service.PtrFromContext[urltest.HistoryStorage](ctx),
		results:        make(map[string]result),
	}
	if s.downloadBytes == 0 {
		s.downloadBytes = defaultDownloadBytes
	}
	if s.timeout <= 0 {
		s.timeout = defaultTimeout
	}
	if s.maxRatio <= 0 {
		s.maxRatio = defaultMaxRTTRatio
	}
	if s.screenInterval <= 0 {
		s.screenInterval = defaultScreenInterval
	}
	if s.checkInterval <= 0 {
		s.checkInterval = defaultCheckInterval
	}
	return s, nil
}

func (s *Service) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStarted {
		return nil
	}
	go s.loop(scope.Context())
	return nil
}

// Register attaches a group; the returned handle is detached by Close.
func (s *Service) Register(group Group, options GroupOptions) *Handle {
	handle := &Handle{service: s, group: group, options: options}
	s.groupsAccess.Lock()
	s.groups = append(s.groups, handle)
	s.groupsAccess.Unlock()
	return handle
}

func (h *Handle) Close() {
	s := h.service
	s.groupsAccess.Lock()
	s.groups = slices.DeleteFunc(s.groups, func(it *Handle) bool { return it == h })
	s.groupsAccess.Unlock()
}

// Trigger asks for a prompt check of this group's selection, e.g. after a
// connection through it failed or its members changed.
func (h *Handle) Trigger() {
	if !h.pending.CompareAndSwap(false, true) {
		return
	}
	select {
	case h.service.triggers <- h:
	default:
		h.pending.Store(false)
	}
}

// NetworkChanged asks for a full screening that reselects every group.
func (h *Handle) NetworkChanged() {
	select {
	case h.service.networkChanged <- struct{}{}:
	default:
	}
}

// Screen requests a shared screening. Requests queued or running together coalesce.
func (h *Handle) Screen() {
	s := h.service
	s.reportsAccess.Lock()
	defer s.reportsAccess.Unlock()
	if s.state.Running || s.screenPending {
		return
	}
	s.screenPending = true
	select {
	case s.screenRequests <- struct{}{}:
	default:
	}
}

// Snapshot atomically reads lifecycle and measurements as this group judges them.
// Missing reports are unchecked, not failed. Request groups never share reports.
func (h *Handle) Snapshot() (State, map[string]Report) {
	s := h.service
	s.reportsAccess.RLock()
	defer s.reportsAccess.RUnlock()
	state := s.state
	reports := s.reports
	if h.options.URL != "" {
		reports = h.reports
	}
	if state.Error == "" {
		state.Error = h.error
	}
	return state, maps.Clone(reports)
}

// URL returns the request URL of a URL group, or empty for a download group.
func (h *Handle) URL() string {
	return h.options.URL
}

// MaxRTTRatio returns the transfer to first byte ratio above which a download disqualifies.
func (h *Handle) MaxRTTRatio() float64 {
	return h.service.maxRatio
}

func (s *Service) handles() []*Handle {
	s.groupsAccess.Lock()
	defer s.groupsAccess.Unlock()
	return slices.Clone(s.groups)
}

func (s *Service) paused() bool {
	return s.pause != nil && (s.pause.IsDevicePaused() || s.pause.IsNetworkPaused())
}

func (s *Service) loop(ctx context.Context) {
	s.screen(ctx, false, "startup")
	// Members settling during startup (providers loading) queue triggers that
	// the startup screening already answered.
	for drained := false; !drained; {
		select {
		case handle := <-s.triggers:
			handle.pending.Store(false)
		default:
			drained = true
		}
	}
	screenTicker := time.NewTicker(s.screenInterval)
	defer screenTicker.Stop()
	checkTicker := time.NewTicker(s.checkInterval)
	defer checkTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-screenTicker.C:
			if !s.paused() {
				s.screen(ctx, false, "interval")
			}
		case <-checkTicker.C:
			if s.paused() {
				continue
			}
			// Until a screening succeeds (e.g. started offline) there is no pool to check against.
			if len(s.results) == 0 {
				s.screen(ctx, false, "retry")
			} else {
				s.check(ctx, s.handles(), "interval", false)
			}
		case <-s.networkChanged:
			select {
			case <-ctx.Done():
				return
			case <-time.After(networkSettle):
			}
			select {
			case <-s.networkChanged:
			default:
			}
			s.domesticAt = time.Time{}
			s.screen(ctx, true, "network changed")
			screenTicker.Reset(s.screenInterval)
		case <-s.screenRequests:
			s.screen(ctx, false, "requested")
			screenTicker.Reset(s.screenInterval)
		case handle := <-s.triggers:
			handle.pending.Store(false)
			if time.Since(handle.lastCheck) >= passiveCooldown && !s.paused() {
				s.check(ctx, s.sharingSelection(handle), "triggered", true)
			}
		}
	}
}

func (s *Service) target() string {
	return s.targets[s.round%len(s.targets)]
}

func (s *Service) download(ctx context.Context, outbound adapter.Outbound, timeout time.Duration) sample {
	return fetch(ctx, outbound, s.target(), s.downloadBytes, timeout)
}

// domesticOK tells a broken local network apart from broken nodes: when the
// domestic URL cannot be fetched directly, nothing is judged or switched.
func (s *Service) domesticOK(ctx context.Context) bool {
	if time.Since(s.domesticAt) < domesticCacheTime {
		return s.domesticPass
	}
	result := fetch(ctx, s.direct, s.domesticURL, s.downloadBytes, s.timeout)
	s.domesticAt, s.domesticPass = time.Now(), result.err == nil
	if result.err != nil {
		s.logger.Warn("domestic check failed, local network treated as down: ", result.err)
	}
	return s.domesticPass
}

// screen downloads through every member of every download group and rebuilds
// the pool. Download groups select the lowest full-precision first-byte plus
// transfer duration; reselect only affects request groups (network changed).
func (s *Service) screen(ctx context.Context, reselect bool, reason string) {
	handles := s.handles()
	s.prune(handles)
	s.reportsAccess.Lock()
	s.state.Running, s.state.Error = true, ""
	s.screenPending = false
	select {
	case <-s.screenRequests:
	default:
	}
	clear(s.reports)
	for _, handle := range handles {
		clear(handle.reports)
		handle.error = ""
	}
	s.reportsAccess.Unlock()
	s.notify()
	var screenError string
	defer func() {
		if ctx.Err() != nil {
			screenError = ctx.Err().Error()
		}
		s.reportsAccess.Lock()
		s.state.Running = false
		s.state.Generation++
		s.state.ScreenedAt = time.Now()
		s.state.Error = screenError
		s.reportsAccess.Unlock()
		s.notify()
	}()
	if s.paused() {
		screenError = "screening skipped: device or network is paused"
		return
	}
	if !s.domesticOK(ctx) {
		screenError = "screening skipped: domestic connectivity check failed"
		return
	}
	s.lastScreen = time.Now()
	var nodes []adapter.Outbound
	seen := make(map[string]bool)
	for _, handle := range handles {
		if handle.options.URL != "" {
			continue
		}
		for _, node := range handle.group.Candidates() {
			if !seen[node.Tag()] {
				seen[node.Tag()] = true
				nodes = append(nodes, node)
			}
		}
	}
	s.round++
	results := make(map[string]result, len(nodes))
	var (
		access    sync.Mutex
		waitGroup sync.WaitGroup
		semaphore = make(chan struct{}, screenConcurrency)
		passed    int
	)
	for _, node := range nodes {
		waitGroup.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer waitGroup.Done()
			defer func() { <-semaphore }()
			measured := s.download(ctx, node, s.timeout)
			ok := !measured.junk(s.maxRatio)
			s.record(nil, node, measured, ok)
			access.Lock()
			defer access.Unlock()
			results[node.Tag()] = result{ok: ok, score: measured.firstByte + measured.transfer}
			if ok {
				passed++
			}
		}()
	}
	waitGroup.Wait()
	if ctx.Err() != nil {
		return
	}
	allFailed := len(nodes) > 0 && passed == 0
	if allFailed {
		s.logger.Warn("screen (", reason, "): every node failed, treated as a network problem")
	} else {
		s.results = results
	}
	s.logger.Info("screen (", reason, "): ", passed, "/", len(nodes), " passed, target ", s.target())
	for _, handle := range handles {
		if handle.options.URL != "" {
			s.checkRequestGroup(ctx, handle, reselect)
			continue
		}
		pool := s.pool(handle, nil)
		if allFailed || len(pool) == 0 {
			s.reportsAccess.Lock()
			handle.error = "no passing download member; selection retained"
			s.reportsAccess.Unlock()
			continue
		}
		current := handle.group.Current()
		if current == pool[0] {
			continue
		}
		handle.group.Choose(pool[0], "screen: lowest download completion time")
	}
}

// prune bounds stored data by the live download members, even between screens.
func (s *Service) prune(handles []*Handle) {
	live := make(map[string]bool)
	for _, handle := range handles {
		if handle.options.URL == "" {
			for _, candidate := range handle.group.Candidates() {
				live[candidate.Tag()] = true
			}
		}
	}
	s.reportsAccess.Lock()
	for tag := range s.reports {
		if !live[tag] {
			delete(s.reports, tag)
		}
	}
	s.reportsAccess.Unlock()
	for tag := range s.results {
		if !live[tag] {
			delete(s.results, tag)
		}
	}
}

func (s *Service) notify() {
	if s.history != nil {
		s.history.NotifyUpdated()
	}
}

// record logs a sample and publishes it as the member's report; handle is the
// URL group a plain request was made for, or nil for a download.
func (s *Service) record(handle *Handle, outbound adapter.Outbound, measured sample, ok bool) {
	report := Report{Time: time.Now(), OK: ok, Ratio: measured.ratio()}
	if measured.err != nil {
		s.logger.Debug(outbound.Tag(), ": ", measured.err)
		report.Error = measured.err.Error()
	} else {
		s.logger.Debug(outbound.Tag(), ": first byte ", measured.firstByte.Milliseconds(), "ms, transfer ",
			measured.transfer.Milliseconds(), "ms, ratio ", int(measured.ratio()*10+0.5), "/10, junk ", measured.junk(s.maxRatio))
		report.FirstByte, report.Transfer = measured.firstByte, measured.transfer
		if !ok {
			report.Error = "transfer to first byte ratio exceeds max_rtt_ratio"
		}
	}
	s.reportsAccess.Lock()
	if handle != nil {
		if handle.reports == nil {
			handle.reports = make(map[string]Report)
		}
		handle.reports[outbound.Tag()] = report
	} else {
		if s.reports == nil {
			s.reports = make(map[string]Report)
		}
		s.reports[outbound.Tag()] = report
	}
	s.reportsAccess.Unlock()
	s.notify()
}

// pool returns passing members by ascending download completion time.
// Equal scores retain candidate order, independent of concurrent completion order.
func (s *Service) pool(handle *Handle, exclude map[string]bool) []adapter.Outbound {
	var pool []adapter.Outbound
	for _, candidate := range handle.group.Candidates() {
		if result, loaded := s.results[candidate.Tag()]; loaded && result.ok && !exclude[candidate.Tag()] {
			pool = append(pool, candidate)
		}
	}
	slices.SortStableFunc(pool, func(a, b adapter.Outbound) int {
		return cmp.Compare(s.results[a.Tag()].score, s.results[b.Tag()].score)
	})
	return pool
}

// check verifies each group's selection and moves a disqualified group along
// its pool. Within one call a node shared by several groups is tested once.
func (s *Service) check(ctx context.Context, handles []*Handle, reason string, triggered bool) {
	s.prune(s.handles())
	verdicts := make(map[string]bool)
	for _, handle := range handles {
		if ctx.Err() != nil {
			return
		}
		s.logger.Debug("check ", handle.group.Tag(), " (", reason, ")")
		handle.lastCheck = time.Now()
		if handle.options.URL != "" {
			s.checkRequestGroup(ctx, handle, false)
		} else {
			s.checkDownloadGroup(ctx, handle, verdicts, triggered)
		}
	}
}

// sharingSelection returns the triggered group followed by every other download
// group on the same member: a node that broke for one of them broke for all.
func (s *Service) sharingSelection(handle *Handle) []*Handle {
	handles := []*Handle{handle}
	current := handle.group.Current()
	if handle.options.URL != "" || current == nil {
		return handles
	}
	for _, other := range s.handles() {
		if other != handle && other.options.URL == "" && other.group.Current() == current {
			handles = append(handles, other)
		}
	}
	return handles
}

// verify downloads through outbound up to attempts times and records the verdict.
func (s *Service) verify(ctx context.Context, outbound adapter.Outbound, verdicts map[string]bool, attempts int, timeout time.Duration) bool {
	if verdict, loaded := verdicts[outbound.Tag()]; loaded {
		return verdict
	}
	for range attempts {
		measured := s.download(ctx, outbound, timeout)
		ok := !measured.junk(s.maxRatio)
		s.record(nil, outbound, measured, ok)
		if ok {
			s.results[outbound.Tag()] = result{ok: true, score: measured.firstByte + measured.transfer}
			verdicts[outbound.Tag()] = true
			return true
		}
	}
	verdicts[outbound.Tag()] = false
	return false
}

// checkDownloadGroup verifies the selection and walks the pool when it fails.
// A scheduled check needs two failures in a row, since one sample alone
// collapses now and then, and verifies a candidate before switching to it. A
// check triggered by a failing connection tests once against a short limit and
// switches to each candidate before verifying it, so traffic leaves a dead node
// at once; the pool was screened recently.
func (s *Service) checkDownloadGroup(ctx context.Context, handle *Handle, verdicts map[string]bool, triggered bool) {
	current := handle.group.Current()
	if current == nil {
		return
	}
	attempts, timeout := 2, s.timeout
	if triggered {
		attempts, timeout = 1, min(triggeredTimeout, s.timeout)
	}
	if s.verify(ctx, current, verdicts, attempts, timeout) || !s.domesticOK(ctx) {
		return
	}
	s.results[current.Tag()] = result{}
	reason := "check: " + current.Tag() + " disqualified"
	if triggered {
		reason = "connection failed, " + current.Tag() + " disqualified"
	}
	tried := map[string]bool{current.Tag(): true}
	for _, candidate := range s.pool(handle, tried) {
		tried[candidate.Tag()] = true
		if triggered {
			handle.group.Choose(candidate, reason)
		}
		if s.verify(ctx, candidate, verdicts, 1, s.timeout) {
			if !triggered {
				handle.group.Choose(candidate, reason)
			}
			return
		}
		s.results[candidate.Tag()] = result{}
		if triggered {
			reason = candidate.Tag() + " disqualified"
		}
	}
	// A degraded network can exhaust several groups in a row; screen at most once per check interval.
	if time.Since(s.lastScreen) < s.checkInterval {
		s.logger.Warn("group ", handle.group.Tag(), ": pool exhausted, keeping ", handle.group.Current().Tag(), " until the next screening")
		return
	}
	s.logger.Warn("group ", handle.group.Tag(), ": pool exhausted, screening")
	s.screen(ctx, false, "pool exhausted")
}

// checkRequestGroup tests every member of a URL group with a plain request.
func (s *Service) checkRequestGroup(ctx context.Context, handle *Handle, reselect bool) {
	s.reportsAccess.Lock()
	clear(handle.reports)
	handle.error = ""
	s.reportsAccess.Unlock()
	defer s.notify()
	current := handle.group.Current()
	var (
		choice        adapter.Outbound
		best          time.Duration
		currentPassed bool
	)
	for _, candidate := range handle.group.Candidates() {
		attempts := 1
		if candidate == current {
			attempts = 2
		}
		var measured sample
		for range attempts {
			measured = fetch(ctx, candidate, handle.options.URL, 0, s.timeout)
			if measured.err == nil {
				break
			}
		}
		s.record(handle, candidate, measured, measured.err == nil)
		if measured.err != nil {
			continue
		}
		if candidate == current {
			currentPassed = true
		}
		if choice == nil || (!handle.options.PreferOrder && measured.firstByte < best) {
			choice, best = candidate, measured.firstByte
		}
		if handle.options.PreferOrder {
			break
		}
	}
	if choice == nil {
		s.reportsAccess.Lock()
		handle.error = "no member reaches request URL; selection retained"
		s.reportsAccess.Unlock()
		if s.domesticOK(ctx) {
			s.logger.Warn("group ", handle.group.Tag(), ": no member reaches ", handle.options.URL)
		}
		return
	}
	if choice == current || (!handle.options.PreferOrder && currentPassed && !reselect) {
		return
	}
	handle.group.Choose(choice, "request check of "+handle.options.URL)
}
