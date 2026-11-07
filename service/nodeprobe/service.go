// Package nodeprobe measures group members through each of them and keeps every
// probe group on the member its configured policy prefers. Measurement,
// statistics and switching are fixed here; what qualifies, how members rank and
// when to switch are expressions in the configuration.
package nodeprobe

import (
	"cmp"
	"context"
	"math"
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

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

const (
	// Interface events arrive in bursts; screen once they settle.
	networkSettle = 2 * time.Second
	// The domestic verdict is reused for this long within one burst of work.
	domesticCacheTime = 30 * time.Second
	// A screening skipped for a paused device or a broken local network is retried this often.
	retryInterval = time.Minute
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

// Report is the latest evaluation of one member by one group.
type Report struct {
	Time     time.Time
	Eligible bool
	// OK means qualified and not penalized.
	OK           bool
	Error        string
	HasScore     bool
	Score        float64
	PenaltyUntil time.Time
}

// State describes the screenings of one policy, including skipped attempts.
type State struct {
	ScreenedAt time.Time
	Running    bool
	Generation uint64
	Error      string
}

type policyState struct {
	State
	nextScreen time.Time
	nextCheck  time.Time
	lastScreen time.Time
	requested  bool
}

type Handle struct {
	service *Service
	group   Group
	policy  *policy
	pending atomic.Bool
	// Owned by the loop goroutine.
	lastCheck time.Time
	// Guarded by service.access.
	reports      map[string]Report
	penalties    map[string]time.Time
	selectedAt   time.Time
	switchReason string
	error        string
}

type domesticCheck struct {
	url     string
	timeout time.Duration
	bytes   uint32
}

type Service struct {
	boxService.Adapter
	logger      log.ContextLogger
	measures    map[string]*measure
	policies    map[string]*policy
	policyOrder []*policy
	domestic    *domesticCheck
	direct      N.Dialer
	pause       pause.Manager
	history     *urltest.HistoryStorage
	samples     sampleStore
	traffic     *trafficStore

	groupsAccess sync.Mutex
	groups       []*Handle

	networkChanged chan struct{}
	triggers       chan *Handle
	screenRequests chan *policy

	access sync.RWMutex
	states map[*policy]*policyState

	// Owned by the loop goroutine.
	rounds       map[string]int
	domesticAt   time.Time
	domesticLast sample
	// Set when a sample was dropped because its member was replaced meanwhile.
	replaced atomic.Bool
}

func NewService(ctx context.Context, logger log.ContextLogger, tag string, options option.NodeProbeServiceOptions) (adapter.Service, error) {
	direct, err := dialer.New(ctx, option.DialerOptions{}, true)
	if err != nil {
		return nil, err
	}
	s, err := newService(logger, tag, options, direct)
	if err != nil {
		return nil, err
	}
	s.pause = service.FromContext[pause.Manager](ctx)
	s.history = service.PtrFromContext[urltest.HistoryStorage](ctx)
	return s, nil
}

func newService(logger log.ContextLogger, tag string, options option.NodeProbeServiceOptions, direct N.Dialer) (*Service, error) {
	if len(options.Measures) == 0 {
		return nil, E.New("missing measures")
	}
	if len(options.Policies) == 0 {
		return nil, E.New("missing policies")
	}
	trafficWindow := time.Duration(options.TrafficWindow)
	if trafficWindow <= 0 {
		trafficWindow = defaultTrafficWindow
	}
	s := &Service{
		Adapter:        boxService.NewAdapter(C.TypeNodeProbe, tag),
		logger:         logger,
		measures:       make(map[string]*measure, len(options.Measures)),
		policies:       make(map[string]*policy, len(options.Policies)),
		direct:         direct,
		traffic:        newTrafficStore(trafficWindow),
		networkChanged: make(chan struct{}, 1),
		triggers:       make(chan *Handle, 64),
		screenRequests: make(chan *policy, len(options.Policies)),
		states:         make(map[*policy]*policyState, len(options.Policies)),
		rounds:         make(map[string]int),
	}
	if options.Domestic != nil {
		if options.Domestic.URL == "" {
			return nil, E.New("domestic: missing url")
		}
		s.domestic = &domesticCheck{url: options.Domestic.URL, timeout: time.Duration(options.Domestic.Timeout), bytes: options.Domestic.Bytes}
		if s.domestic.timeout <= 0 {
			s.domestic.timeout = defaultDomesticTime
		}
	}
	for name, measureOptions := range options.Measures {
		m, err := newMeasure(name, measureOptions)
		if err != nil {
			return nil, err
		}
		s.measures[name] = m
	}
	for name, policyOptions := range options.Policies {
		p, err := newPolicy(name, policyOptions, s.measures)
		if err != nil {
			return nil, err
		}
		s.policies[name] = p
		s.policyOrder = append(s.policyOrder, p)
		s.states[p] = &policyState{}
	}
	slices.SortFunc(s.policyOrder, func(a, b *policy) int { return cmp.Compare(a.name, b.name) })
	return s, nil
}

func (s *Service) Start(stage adapter.StartStage, scope *adapter.Scope) error {
	if stage != adapter.StartStateStarted {
		return nil
	}
	go s.loop(scope.Context())
	return nil
}

// Register attaches a group to a policy; the returned handle is detached by Close.
func (s *Service) Register(group Group, policyName string) (*Handle, error) {
	p := s.policies[policyName]
	if p == nil {
		return nil, E.New("policy not found: ", policyName)
	}
	handle := &Handle{service: s, group: group, policy: p}
	s.groupsAccess.Lock()
	s.groups = append(s.groups, handle)
	s.groupsAccess.Unlock()
	return handle, nil
}

func (h *Handle) Close() {
	s := h.service
	s.groupsAccess.Lock()
	s.groups = slices.DeleteFunc(s.groups, func(it *Handle) bool { return it == h })
	s.groupsAccess.Unlock()
}

// Trigger asks for a prompt check of this group's selection, e.g. after its
// members changed; a policy without check screens instead.
func (h *Handle) Trigger() {
	if h.policy.checkMeasures == nil {
		h.Screen()
		return
	}
	if !h.pending.CompareAndSwap(false, true) {
		return
	}
	select {
	case h.service.triggers <- h:
	default:
		h.pending.Store(false)
	}
}

// NetworkChanged asks for a screening of every policy that screens on network changes.
func (h *Handle) NetworkChanged() {
	select {
	case h.service.networkChanged <- struct{}{}:
	default:
	}
}

// Screen requests a screening of this group's policy. Requests queued or
// running together coalesce.
func (h *Handle) Screen() {
	s := h.service
	s.access.Lock()
	defer s.access.Unlock()
	state := s.states[h.policy]
	if state.Running || state.requested {
		return
	}
	state.requested = true
	select {
	case s.screenRequests <- h.policy:
	default:
	}
}

// ConnectionStarted counts a routed connection that reached node.
func (h *Handle) ConnectionStarted(node string) {
	h.service.traffic.started(node, time.Now())
}

// ConnectionDialFailed counts a connection that could not be opened through node.
func (h *Handle) ConnectionDialFailed(node string) {
	h.service.traffic.dialFailed(node, time.Now())
	h.trafficFailed(node)
}

// ConnectionEnded counts the end of a connection through node.
func (h *Handle) ConnectionEnded(node string, failed bool, down, up int64) {
	h.service.traffic.ended(node, failed, down, up, time.Now())
	if failed {
		h.trafficFailed(node)
	}
}

func (h *Handle) trafficFailed(node string) {
	if !h.policy.onTrafficFailure {
		return
	}
	if current := h.group.Current(); current == nil || current.Tag() != node {
		return
	}
	h.Trigger()
}

// Snapshot is a consistent view of one group for the API.
type Snapshot struct {
	State
	Policy         string
	Expressions    []Expression
	ScreenMeasures []string
	CheckMeasures  []string
	SelectedAt     time.Time
	SwitchReason   string
	Reports        map[string]Report
	// Measures maps a member to its statistics per measure of the policy.
	Measures      map[string]map[string]MeasureStats
	MeasureKinds  map[string]string
	Traffic       map[string]TrafficStats
	TrafficWindow time.Duration
}

func (h *Handle) Snapshot() Snapshot {
	s := h.service
	now := time.Now()
	s.access.RLock()
	snapshot := Snapshot{
		State:          s.states[h.policy].State,
		Policy:         h.policy.name,
		Expressions:    h.policy.expressions,
		ScreenMeasures: h.policy.screenMeasures,
		CheckMeasures:  h.policy.checkMeasures,
		SelectedAt:     h.selectedAt,
		SwitchReason:   h.switchReason,
		Reports:        make(map[string]Report, len(h.reports)),
	}
	for tag, report := range h.reports {
		snapshot.Reports[tag] = report
	}
	if h.error != "" && !snapshot.Running {
		snapshot.Error = h.error
	}
	s.access.RUnlock()
	candidates := h.group.Candidates()
	snapshot.Measures = make(map[string]map[string]MeasureStats, len(candidates))
	snapshot.Traffic = make(map[string]TrafficStats, len(candidates))
	for _, candidate := range candidates {
		snapshot.Measures[candidate.Tag()] = s.samples.stats(h.policy.measures, candidate.Tag(), now)
		snapshot.Traffic[candidate.Tag()] = s.traffic.stats(candidate.Tag(), now)
	}
	snapshot.MeasureKinds = make(map[string]string, len(h.policy.measures))
	for _, name := range h.policy.measures {
		snapshot.MeasureKinds[name] = s.measures[name].kind
	}
	snapshot.TrafficWindow = s.traffic.window()
	return snapshot
}

func (s *Service) handles() []*Handle {
	s.groupsAccess.Lock()
	defer s.groupsAccess.Unlock()
	return slices.Clone(s.groups)
}

func (s *Service) handlesOf(p *policy) []*Handle {
	var handles []*Handle
	for _, handle := range s.handles() {
		if handle.policy == p {
			handles = append(handles, handle)
		}
	}
	return handles
}

func (s *Service) paused() bool {
	return s.pause != nil && (s.pause.IsDevicePaused() || s.pause.IsNetworkPaused())
}

func (s *Service) notify() {
	if s.history != nil {
		s.history.NotifyUpdated()
	}
}

func (s *Service) loop(ctx context.Context) {
	s.screen(ctx, s.policyOrder, "startup")
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
	timer := time.NewTimer(s.untilDue())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.runDue(ctx)
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
			var policies []*policy
			for _, p := range s.policyOrder {
				if p.onNetworkChange {
					policies = append(policies, p)
				}
			}
			s.screen(ctx, policies, "network changed")
		case p := <-s.screenRequests:
			s.screen(ctx, []*policy{p}, "requested")
		case handle := <-s.triggers:
			handle.pending.Store(false)
			if time.Since(handle.lastCheck) >= handle.policy.cooldown && !s.paused() {
				s.check(ctx, handle, "connection failed", make(map[string]bool))
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(s.untilDue())
	}
}

func (s *Service) untilDue() time.Duration {
	s.access.RLock()
	defer s.access.RUnlock()
	next := time.Duration(math.MaxInt64)
	now := time.Now()
	for _, p := range s.policyOrder {
		state := s.states[p]
		next = min(next, state.nextScreen.Sub(now))
		if p.checkMeasures != nil {
			next = min(next, state.nextCheck.Sub(now))
		}
	}
	return max(next, 0)
}

func (s *Service) runDue(ctx context.Context) {
	now := time.Now()
	var screens, checks []*policy
	s.access.RLock()
	for _, p := range s.policyOrder {
		state := s.states[p]
		if !now.Before(state.nextScreen) {
			screens = append(screens, p)
		} else if p.checkMeasures != nil && !now.Before(state.nextCheck) {
			checks = append(checks, p)
		}
	}
	s.access.RUnlock()
	if len(screens) > 0 {
		s.screen(ctx, screens, "interval")
	}
	for _, p := range checks {
		s.access.Lock()
		s.states[p].nextCheck = now.Add(p.checkInterval)
		s.access.Unlock()
		if s.paused() {
			continue
		}
		measured := make(map[string]bool)
		for _, handle := range s.handlesOf(p) {
			if ctx.Err() != nil {
				return
			}
			s.check(ctx, handle, "interval", measured)
		}
	}
}

// domesticOK tells a broken local network apart from broken nodes: when the
// domestic URL cannot be fetched directly, nothing is judged or switched.
func (s *Service) domesticOK(ctx context.Context) bool {
	if s.domestic == nil {
		return true
	}
	if time.Since(s.domesticAt) < domesticCacheTime {
		return s.domesticLast.err == nil
	}
	s.domesticLast = fetch(ctx, s.direct, s.domestic.url, s.domestic.bytes, s.domestic.timeout)
	s.domesticAt = time.Now()
	if s.domesticLast.err != nil {
		s.logger.Warn("domestic check failed, local network treated as down: ", s.domesticLast.err)
	}
	return s.domesticLast.err == nil
}

func (s *Service) domesticInfo(now time.Time) DomesticInfo {
	info := DomesticInfo{OK: s.domestic == nil, Configured: s.domestic != nil, Age: math.Inf(1)}
	if s.domestic == nil || s.domesticAt.IsZero() {
		return info
	}
	last := s.domesticLast
	info.Age = now.Sub(s.domesticAt).Seconds()
	if last.err != nil {
		info.LastError = last.err.Error()
		return info
	}
	info.OK = true
	info.FirstByte, info.Transfer = last.firstByte.Seconds(), last.transfer.Seconds()
	info.Total = info.FirstByte + info.Transfer
	if last.bytes > 0 {
		info.Throughput = float64(last.bytes) / max(last.transfer, time.Millisecond).Seconds()
	}
	return info
}

// measureNode runs one measure through node for every target of this round.
func (s *Service) measureNode(ctx context.Context, m *measure, node adapter.Outbound) {
	size := uint32(0)
	if m.kind == kindDownload {
		size = m.bytes
	}
	for _, target := range m.roundTargets(s.rounds[m.name]) {
		for range m.samples {
			if ctx.Err() != nil {
				return
			}
			measured := fetch(ctx, node, target, size, m.timeout)
			r := record{time: time.Now(), target: target, firstByte: measured.firstByte, transfer: measured.transfer, bytes: measured.bytes}
			if measured.err != nil {
				if !s.live(node) {
					// A provider refresh replaced and closed this member mid-measure;
					// the failure says nothing about the node behind the tag.
					s.replaced.Store(true)
					return
				}
				r.err = measured.err.Error()
				s.logger.Debug(m.name, " ", node.Tag(), ": ", measured.err)
			} else {
				s.logger.Debug(m.name, " ", node.Tag(), ": first byte ", measured.firstByte.Milliseconds(), "ms, transfer ", measured.transfer.Milliseconds(), "ms")
			}
			s.samples.add(m.name, node.Tag(), m.window, r)
		}
	}
}

// live reports whether node is still a member of some group.
func (s *Service) live(node adapter.Outbound) bool {
	for _, handle := range s.handles() {
		if slices.Contains(handle.group.Candidates(), node) {
			return true
		}
	}
	return false
}

// measureAll runs one measure through nodes with the measure's concurrency.
func (s *Service) measureAll(ctx context.Context, m *measure, nodes []adapter.Outbound) {
	var waitGroup sync.WaitGroup
	semaphore := make(chan struct{}, m.concurrency)
	for _, node := range nodes {
		waitGroup.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer waitGroup.Done()
			defer func() { <-semaphore }()
			s.measureNode(ctx, m, node)
		}()
	}
	waitGroup.Wait()
	s.notify()
}

func (s *Service) prune() {
	live := make(map[string]bool)
	for _, handle := range s.handles() {
		for _, candidate := range handle.group.Candidates() {
			live[candidate.Tag()] = true
		}
	}
	s.samples.prune(live)
	s.traffic.prune(live)
}

// screen measures the eligible members of every group of policies and lets
// each group select by its policy.
func (s *Service) screen(ctx context.Context, policies []*policy, reason string) {
	if len(policies) == 0 {
		return
	}
	now := time.Now()
	s.access.Lock()
	for _, p := range policies {
		state := s.states[p]
		state.Running, state.Error, state.requested = true, "", false
	}
	s.access.Unlock()
	s.notify()
	screenError := ""
	defer func() {
		if ctx.Err() != nil {
			screenError = ctx.Err().Error()
		}
		finished := time.Now()
		s.access.Lock()
		for _, p := range policies {
			state := s.states[p]
			state.Running = false
			state.Generation++
			state.ScreenedAt = finished
			state.Error = screenError
			if screenError == "" {
				state.lastScreen = finished
				state.nextScreen = finished.Add(p.screenInterval)
			} else {
				state.nextScreen = finished.Add(min(retryInterval, p.screenInterval))
			}
			state.nextCheck = finished.Add(p.checkInterval)
		}
		s.access.Unlock()
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
	s.prune()
	var handles []*Handle
	for _, p := range policies {
		handles = append(handles, s.handlesOf(p)...)
	}
	// Measure the union of eligible members per measure, once each.
	nodesByMeasure := make(map[string][]adapter.Outbound)
	seen := make(map[string]map[string]bool)
	for _, handle := range handles {
		group := s.groupContext(handle, now)
		for index, candidate := range group.candidates {
			if !s.eligible(handle, group, index, now) {
				continue
			}
			for _, name := range handle.policy.screenMeasures {
				if seen[name] == nil {
					seen[name] = make(map[string]bool)
				}
				if !seen[name][candidate.Tag()] {
					seen[name][candidate.Tag()] = true
					nodesByMeasure[name] = append(nodesByMeasure[name], candidate)
				}
			}
		}
	}
	names := make([]string, 0, len(nodesByMeasure))
	for name := range nodesByMeasure {
		names = append(names, name)
	}
	slices.Sort(names)
	s.replaced.Store(false)
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		m := s.measures[name]
		s.measureAll(ctx, m, nodesByMeasure[name])
		s.rounds[name]++
	}
	if ctx.Err() != nil {
		return
	}
	if s.replaced.Swap(false) {
		// Members were replaced mid-screening: measure the new ones before judging.
		s.logger.Info("screen (", reason, "): members replaced during measurement, measuring again")
		s.screen(ctx, policies, reason+", members replaced")
		return
	}
	for _, handle := range handles {
		s.reselect(handle, reason)
	}
	s.logger.Info("screen (", reason, "): ", len(handles), " group(s)")
}

type evaluation struct {
	node   adapter.Outbound
	index  int
	report Report
	view   View
}

// groupContext is a group's members with their statistics at one instant,
// shared by the evaluations of one pass.
type groupContext struct {
	candidates []adapter.Outbound
	current    adapter.Outbound
	members    []Member
	peers      map[string]PeerStats
	domestic   DomesticInfo
}

func (s *Service) groupContext(handle *Handle, now time.Time) *groupContext {
	candidates := handle.group.Candidates()
	group := &groupContext{
		candidates: candidates,
		current:    handle.group.Current(),
		members:    make([]Member, len(candidates)),
		domestic:   s.domesticInfo(now),
	}
	s.access.RLock()
	for index, candidate := range candidates {
		group.members[index].Penalized = now.Before(handle.penalties[candidate.Tag()])
	}
	s.access.RUnlock()
	for index, candidate := range candidates {
		member := &group.members[index]
		member.Tag, member.Index, member.Type = candidate.Tag(), index, candidate.Type()
		member.IsCurrent = candidate == group.current
		member.M = s.samples.stats(handle.policy.measures, member.Tag, now)
		member.Traffic = s.traffic.stats(member.Tag, now)
	}
	group.peers = peerStats(group.members, handle.policy.measures)
	return group
}

func (s *Service) nodeEnv(handle *Handle, group *groupContext, index int, now time.Time) NodeEnv {
	member := group.members[index]
	env := NodeEnv{
		Units:     units,
		M:         member.M,
		Traffic:   member.Traffic,
		Node:      NodeInfo{Tag: member.Tag, Index: index, Type: member.Type},
		IsCurrent: member.IsCurrent,
		Domestic:  group.domestic,
		Now:       nowInfo(now),
		Group:     GroupInfo{Tag: handle.group.Tag(), Size: len(group.members)},
		Members:   group.members,
		Peers:     group.peers,
	}
	if group.current != nil {
		env.Group.Current = group.current.Tag()
	}
	s.access.RLock()
	if env.IsCurrent && !handle.selectedAt.IsZero() {
		env.SelectedFor = now.Sub(handle.selectedAt).Seconds()
	}
	if until := handle.penalties[member.Tag]; now.Before(until) {
		env.Penalized, env.PenaltyLeft = true, until.Sub(now).Seconds()
	}
	s.access.RUnlock()
	return env
}

func (s *Service) eligible(handle *Handle, group *groupContext, index int, now time.Time) bool {
	ok, err := runBool(handle.policy.eligible, s.nodeEnv(handle, group, index, now))
	return err == nil && ok
}

// evaluateNode judges node against the group's statistics of now; ok is false
// when node is no longer a member.
func (s *Service) evaluateNode(handle *Handle, node adapter.Outbound) (evaluation, bool) {
	now := time.Now()
	group := s.groupContext(handle, now)
	index := slices.Index(group.candidates, node)
	if index < 0 {
		return evaluation{}, false
	}
	return s.evaluate(handle, group, index, now), true
}

// evaluate judges one member by the group's policy from the stored statistics
// and publishes the verdict.
func (s *Service) evaluate(handle *Handle, group *groupContext, index int, now time.Time) (result evaluation) {
	p := handle.policy
	node := group.candidates[index]
	env := s.nodeEnv(handle, group, index, now)
	report := Report{Time: now}
	result = evaluation{node: node, index: index}
	defer func() {
		result.report = report
		result.view = View{
			Tag: env.Node.Tag, Index: index, Type: env.Node.Type, IsCurrent: env.IsCurrent,
			Eligible: report.Eligible, Qualified: report.OK, HasScore: report.HasScore, Score: report.Score,
			Penalized: env.Penalized, M: env.M, Traffic: env.Traffic,
		}
		s.access.Lock()
		if handle.reports == nil {
			handle.reports = make(map[string]Report)
		}
		handle.reports[node.Tag()] = report
		s.access.Unlock()
	}()
	eligible, err := runBool(p.eligible, env)
	if err != nil {
		report.Error = "eligible: " + err.Error()
		return result
	}
	if !eligible {
		return result
	}
	report.Eligible = true
	qualified := true
	for _, c := range p.qualify {
		ok, err := runBool(c.program, env)
		if err != nil {
			qualified, report.Error = false, "qualify: "+err.Error()
			break
		}
		if !ok {
			qualified, report.Error = false, "qualify: "+c.source
			break
		}
	}
	score, err := runFloat(p.score, env)
	if err != nil {
		qualified = false
		if report.Error == "" {
			report.Error = "score: " + err.Error()
		}
	} else {
		report.HasScore, report.Score = true, score
	}
	penaltyEnv := PenaltyEnv{NodeEnv: env, Qualified: qualified}
	if qualified {
		penaltyEnv.Score = score
	}
	penalty, err := runFloat(p.penalty, penaltyEnv)
	if err != nil {
		s.logger.Warn("group ", handle.group.Tag(), ": ", node.Tag(), ": penalty: ", err)
	} else if penalty > 0 && !env.Penalized {
		until := now.Add(time.Duration(penalty * float64(time.Second)))
		s.access.Lock()
		if handle.penalties == nil {
			handle.penalties = make(map[string]time.Time)
		}
		handle.penalties[node.Tag()] = until
		s.access.Unlock()
		env.Penalized, env.PenaltyLeft = true, penalty
		s.logger.Info("group ", handle.group.Tag(), ": ", node.Tag(), " penalized for ", time.Duration(penalty*float64(time.Second)).Round(time.Second))
	}
	if env.Penalized {
		report.PenaltyUntil = now.Add(time.Duration(env.PenaltyLeft * float64(time.Second)))
		if qualified && report.Error == "" {
			report.Error = "penalized"
		}
	}
	report.OK = qualified && !env.Penalized
	return result
}

func better(a, b evaluation) bool {
	if a.report.Score != b.report.Score {
		return a.report.Score < b.report.Score
	}
	return a.index < b.index
}

// reselect evaluates every member and switches to the best one when the
// policy's switch expression agrees.
func (s *Service) reselect(handle *Handle, reason string) {
	now := time.Now()
	group := s.groupContext(handle, now)
	candidates := group.candidates
	evaluations := make([]evaluation, 0, len(candidates))
	var best, current *evaluation
	s.access.Lock()
	handle.reports = make(map[string]Report, len(candidates))
	handle.error = ""
	s.access.Unlock()
	currentNode := group.current
	for index := range candidates {
		evaluations = append(evaluations, s.evaluate(handle, group, index, now))
	}
	for i := range evaluations {
		e := &evaluations[i]
		if e.node == currentNode {
			current = e
		}
		if e.report.OK && (best == nil || better(*e, *best)) {
			best = e
		}
	}
	defer s.notify()
	if best == nil {
		s.setError(handle, "no qualified member; selection retained")
		s.logger.Warn("group ", handle.group.Tag(), ": no qualified member, keeping current selection")
		return
	}
	if current == nil || !current.report.Eligible {
		s.choose(handle, best.node, "screen ("+reason+"): best qualified member")
		return
	}
	if best == current {
		return
	}
	members := make([]View, len(evaluations))
	for i, e := range evaluations {
		members[i] = e.view
	}
	switchEnv := SwitchEnv{
		Units:    units,
		Current:  current.view,
		Best:     best.view,
		Members:  members,
		Peers:    group.peers,
		Domestic: group.domestic,
		Now:      nowInfo(now),
		Group:    GroupInfo{Tag: handle.group.Tag(), Size: len(candidates), Current: currentNode.Tag()},
	}
	ok, err := runBool(handle.policy.switchTo, switchEnv)
	if err != nil {
		s.setError(handle, "switch: "+err.Error())
		return
	}
	if ok {
		s.choose(handle, best.node, "screen ("+reason+"): switch expression holds")
	}
}

// check measures the current member and, when it no longer qualifies, walks
// the other members in score order, switching to the first that qualifies
// after being measured.
func (s *Service) check(ctx context.Context, handle *Handle, reason string, measured map[string]bool) {
	p := handle.policy
	handle.lastCheck = time.Now()
	current := handle.group.Current()
	if current == nil {
		return
	}
	if !slices.Contains(handle.group.Candidates(), current) {
		return
	}
	s.logger.Debug("check ", handle.group.Tag(), " (", reason, ")")
	s.measureFor(ctx, p, current, measured)
	if ctx.Err() != nil {
		return
	}
	verdict, loaded := s.evaluateNode(handle, current)
	if !loaded {
		return
	}
	s.notify()
	if verdict.report.OK || !s.domesticOK(ctx) {
		return
	}
	s.access.RLock()
	reports := make(map[string]Report, len(handle.reports))
	for tag, report := range handle.reports {
		reports[tag] = report
	}
	s.access.RUnlock()
	type ranked struct {
		node   adapter.Outbound
		report Report
	}
	var order []ranked
	for _, candidate := range handle.group.Candidates() {
		if candidate == current {
			continue
		}
		report, loaded := reports[candidate.Tag()]
		if loaded && !report.Eligible {
			continue
		}
		order = append(order, ranked{candidate, report})
	}
	slices.SortStableFunc(order, func(a, b ranked) int {
		if a.report.OK != b.report.OK {
			if a.report.OK {
				return -1
			}
			return 1
		}
		if a.report.HasScore != b.report.HasScore {
			if a.report.HasScore {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.report.Score, b.report.Score)
	})
	for _, candidate := range order {
		if ctx.Err() != nil {
			return
		}
		s.measureFor(ctx, p, candidate.node, measured)
		e, loaded := s.evaluateNode(handle, candidate.node)
		s.notify()
		if loaded && e.report.OK {
			s.choose(handle, candidate.node, "check ("+reason+"): "+current.Tag()+" disqualified: "+verdict.report.Error)
			return
		}
	}
	s.setError(handle, "check: no qualified member; selection retained")
	s.access.RLock()
	lastScreen := s.states[p].lastScreen
	s.access.RUnlock()
	if time.Since(lastScreen) < p.checkInterval {
		s.logger.Warn("group ", handle.group.Tag(), ": no qualified member, keeping ", current.Tag(), " until the next screening")
		return
	}
	s.logger.Warn("group ", handle.group.Tag(), ": no qualified member, screening")
	s.screen(ctx, []*policy{p}, "check exhausted")
}

func (s *Service) measureFor(ctx context.Context, p *policy, node adapter.Outbound, measured map[string]bool) {
	for _, name := range p.checkMeasures {
		key := name + "\x00" + node.Tag()
		if measured[key] {
			continue
		}
		measured[key] = true
		s.measureNode(ctx, s.measures[name], node)
	}
}

func (s *Service) choose(handle *Handle, node adapter.Outbound, reason string) {
	handle.group.Choose(node, reason)
	s.access.Lock()
	handle.selectedAt = time.Now()
	handle.switchReason = reason
	handle.error = ""
	s.access.Unlock()
	s.notify()
}

func (s *Service) setError(handle *Handle, message string) {
	s.access.Lock()
	handle.error = message
	s.access.Unlock()
}

func runBool(program *vm.Program, env any) (bool, error) {
	output, err := expr.Run(program, env)
	if err != nil {
		return false, err
	}
	value, _ := output.(bool)
	return value, nil
}

func runFloat(program *vm.Program, env any) (float64, error) {
	output, err := expr.Run(program, env)
	if err != nil {
		return 0, err
	}
	switch value := output.(type) {
	case float64:
		return value, nil
	case int:
		return float64(value), nil
	}
	return 0, E.New("not a number: ", output)
}
