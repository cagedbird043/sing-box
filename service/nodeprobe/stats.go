package nodeprobe

import (
	"math"
	"slices"
	"sync"
	"time"
)

// Units are constants every expression can use; durations are in seconds.
type Units struct {
	MS     float64 `expr:"ms"`
	S      float64 `expr:"s"`
	Minute float64 `expr:"minute"`
	Hour   float64 `expr:"hour"`
	KiB    float64 `expr:"KiB"`
	MiB    float64 `expr:"MiB"`
}

var units = Units{MS: 0.001, S: 1, Minute: 60, Hour: 3600, KiB: 1024, MiB: 1024 * 1024}

// Dist summarizes the successful samples of a window; all zero without one.
type Dist struct {
	Last float64 `expr:"last"`
	P50  float64 `expr:"p50"`
	P90  float64 `expr:"p90"`
	Min  float64 `expr:"min"`
	Max  float64 `expr:"max"`
	Mean float64 `expr:"mean"`
}

type MeasureStats struct {
	Count      int     `expr:"count"`
	OKRate     float64 `expr:"ok_rate"`
	FailStreak int     `expr:"fail_streak"`
	// Age is seconds since the latest sample, +Inf without one.
	Age        float64 `expr:"age"`
	LastOK     bool    `expr:"last_ok"`
	LastError  string  `expr:"last_error"`
	LastTime   time.Time
	FirstByte  Dist                    `expr:"first_byte"`
	Transfer   Dist                    `expr:"transfer"`
	Total      Dist                    `expr:"total"`
	Throughput Dist                    `expr:"throughput"`
	Targets    map[string]MeasureStats `expr:"targets"`
}

type TrafficStats struct {
	Conns      int     `expr:"conns"`
	OK         int     `expr:"ok"`
	DialFailed int     `expr:"dial_failed"`
	Aborted    int     `expr:"aborted"`
	FailRate   float64 `expr:"fail_rate"`
	BytesDown  int64   `expr:"bytes_down"`
	BytesUp    int64   `expr:"bytes_up"`
	// LastFailureAge is seconds since the latest failure, +Inf without one.
	LastFailureAge float64 `expr:"last_failure_age"`
}

type NodeInfo struct {
	Tag   string `expr:"tag"`
	Index int    `expr:"index"`
	Type  string `expr:"type"`
}

// DomesticInfo is the latest direct fetch of the domestic URL; its timings
// are zero until a fetch succeeds.
type DomesticInfo struct {
	OK         bool    `expr:"ok"`
	Configured bool    `expr:"configured"`
	FirstByte  float64 `expr:"first_byte"`
	Transfer   float64 `expr:"transfer"`
	Total      float64 `expr:"total"`
	Throughput float64 `expr:"throughput"`
	// Age is seconds since the latest fetch, +Inf without one.
	Age       float64 `expr:"age"`
	LastError string  `expr:"last_error"`
}

type NowInfo struct {
	Hour    int   `expr:"hour"`
	Minute  int   `expr:"minute"`
	Weekday int   `expr:"weekday"`
	Unix    int64 `expr:"unix"`
}

type GroupInfo struct {
	Tag     string `expr:"tag"`
	Size    int    `expr:"size"`
	Current string `expr:"current"`
}

// Member is one group member's statistics, without verdicts.
type Member struct {
	Tag       string                  `expr:"tag"`
	Index     int                     `expr:"index"`
	Type      string                  `expr:"type"`
	IsCurrent bool                    `expr:"is_current"`
	Penalized bool                    `expr:"penalized"`
	M         map[string]MeasureStats `expr:"m"`
	Traffic   TrafficStats            `expr:"traffic"`
}

// PeerStats summarizes one measure across the group members holding samples.
// OKRate is over their ok_rate; the timing and throughput distributions are
// over the p50 of the members with a successful sample.
type PeerStats struct {
	Count      int  `expr:"count"`
	OK         int  `expr:"ok"`
	OKRate     Dist `expr:"ok_rate"`
	FirstByte  Dist `expr:"first_byte"`
	Transfer   Dist `expr:"transfer"`
	Total      Dist `expr:"total"`
	Throughput Dist `expr:"throughput"`
}

// NodeEnv is the environment of eligible, qualify and score.
type NodeEnv struct {
	Units
	M           map[string]MeasureStats `expr:"m"`
	Traffic     TrafficStats            `expr:"traffic"`
	Node        NodeInfo                `expr:"node"`
	IsCurrent   bool                    `expr:"is_current"`
	SelectedFor float64                 `expr:"selected_for"`
	Penalized   bool                    `expr:"penalized"`
	PenaltyLeft float64                 `expr:"penalty_left"`
	Domestic    DomesticInfo            `expr:"domestic"`
	Now         NowInfo                 `expr:"now"`
	Group       GroupInfo               `expr:"group"`
	Members     []Member                `expr:"members"`
	Peers       map[string]PeerStats    `expr:"peers"`
}

// PenaltyEnv adds this evaluation's verdict for penalty.
type PenaltyEnv struct {
	NodeEnv
	Qualified bool    `expr:"qualified"`
	Score     float64 `expr:"score"`
}

type View struct {
	Tag       string                  `expr:"tag"`
	Index     int                     `expr:"index"`
	Type      string                  `expr:"type"`
	IsCurrent bool                    `expr:"is_current"`
	Eligible  bool                    `expr:"eligible"`
	Qualified bool                    `expr:"qualified"`
	HasScore  bool                    `expr:"has_score"`
	Score     float64                 `expr:"score"`
	Penalized bool                    `expr:"penalized"`
	M         map[string]MeasureStats `expr:"m"`
	Traffic   TrafficStats            `expr:"traffic"`
}

// SwitchEnv is the environment of switch.
type SwitchEnv struct {
	Units
	Current  View                 `expr:"current"`
	Best     View                 `expr:"best"`
	Members  []View               `expr:"members"`
	Peers    map[string]PeerStats `expr:"peers"`
	Domestic DomesticInfo         `expr:"domestic"`
	Now      NowInfo              `expr:"now"`
	Group    GroupInfo            `expr:"group"`
}

func peerStats(members []Member, measures []string) map[string]PeerStats {
	result := make(map[string]PeerStats, len(measures))
	var okRate, firstByte, transfer, total, throughput []float64
	for _, name := range measures {
		okRate, firstByte, transfer, total, throughput = okRate[:0], firstByte[:0], transfer[:0], total[:0], throughput[:0]
		for _, member := range members {
			stats := member.M[name]
			if stats.Count == 0 {
				continue
			}
			okRate = append(okRate, stats.OKRate)
			if stats.OKRate == 0 {
				continue
			}
			firstByte = append(firstByte, stats.FirstByte.P50)
			transfer = append(transfer, stats.Transfer.P50)
			total = append(total, stats.Total.P50)
			throughput = append(throughput, stats.Throughput.P50)
		}
		result[name] = PeerStats{
			Count:      len(okRate),
			OK:         len(throughput),
			OKRate:     distribution(okRate),
			FirstByte:  distribution(firstByte),
			Transfer:   distribution(transfer),
			Total:      distribution(total),
			Throughput: distribution(throughput),
		}
	}
	return result
}

func nowInfo(now time.Time) NowInfo {
	return NowInfo{Hour: now.Hour(), Minute: now.Minute(), Weekday: int(now.Weekday()), Unix: now.Unix()}
}

type record struct {
	time      time.Time
	target    string
	err       string
	firstByte time.Duration
	transfer  time.Duration
	bytes     uint32
}

func (r record) ok() bool { return r.err == "" }

// sampleStore keeps the latest window of samples per measure and node.
type sampleStore struct {
	access  sync.RWMutex
	samples map[string]map[string][]record
}

func (s *sampleStore) add(measure, node string, window int, r record) {
	s.access.Lock()
	defer s.access.Unlock()
	if s.samples == nil {
		s.samples = make(map[string]map[string][]record)
	}
	nodes := s.samples[measure]
	if nodes == nil {
		nodes = make(map[string][]record)
		s.samples[measure] = nodes
	}
	records := append(nodes[node], r)
	if len(records) > window {
		records = slices.Delete(records, 0, len(records)-window)
	}
	nodes[node] = records
}

func (s *sampleStore) stats(measures []string, node string, now time.Time) map[string]MeasureStats {
	s.access.RLock()
	defer s.access.RUnlock()
	result := make(map[string]MeasureStats, len(measures))
	for _, measure := range measures {
		result[measure] = computeStats(s.samples[measure][node], now, true)
	}
	return result
}

func (s *sampleStore) prune(live map[string]bool) {
	s.access.Lock()
	defer s.access.Unlock()
	for _, nodes := range s.samples {
		for node := range nodes {
			if !live[node] {
				delete(nodes, node)
			}
		}
	}
}

func computeStats(records []record, now time.Time, withTargets bool) MeasureStats {
	stats := MeasureStats{Count: len(records), Age: math.Inf(1)}
	if len(records) == 0 {
		return stats
	}
	var firstByte, transfer, total, throughput []float64
	ok := 0
	for _, r := range records {
		if !r.ok() {
			continue
		}
		ok++
		firstByte = append(firstByte, r.firstByte.Seconds())
		transfer = append(transfer, r.transfer.Seconds())
		total = append(total, (r.firstByte + r.transfer).Seconds())
		if r.bytes > 0 {
			throughput = append(throughput, float64(r.bytes)/max(r.transfer, time.Millisecond).Seconds())
		} else {
			throughput = append(throughput, 0)
		}
	}
	for i := len(records) - 1; i >= 0 && !records[i].ok(); i-- {
		stats.FailStreak++
	}
	last := records[len(records)-1]
	stats.OKRate = float64(ok) / float64(len(records))
	stats.LastTime = last.time
	stats.Age = now.Sub(last.time).Seconds()
	stats.LastOK = last.ok()
	stats.LastError = last.err
	stats.FirstByte = distribution(firstByte)
	stats.Transfer = distribution(transfer)
	stats.Total = distribution(total)
	stats.Throughput = distribution(throughput)
	if withTargets {
		byTarget := make(map[string][]record)
		for _, r := range records {
			byTarget[r.target] = append(byTarget[r.target], r)
		}
		stats.Targets = make(map[string]MeasureStats, len(byTarget))
		for target, targetRecords := range byTarget {
			stats.Targets[target] = computeStats(targetRecords, now, false)
		}
	}
	return stats
}

func distribution(values []float64) Dist {
	if len(values) == 0 {
		return Dist{}
	}
	dist := Dist{Last: values[len(values)-1]}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	var sum float64
	for _, value := range sorted {
		sum += value
	}
	dist.Min, dist.Max, dist.Mean = sorted[0], sorted[len(sorted)-1], sum/float64(len(sorted))
	dist.P50, dist.P90 = rank(sorted, 0.5), rank(sorted, 0.9)
	return dist
}

// rank returns the nearest-rank percentile of sorted values.
func rank(sorted []float64, p float64) float64 {
	index := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(index, 0)]
}

// trafficStore counts real connections per node in fixed-width buckets
// covering the traffic window.
type trafficStore struct {
	access      sync.Mutex
	width       time.Duration
	size        int
	nodes       map[string][]trafficBucket
	lastFailure map[string]time.Time
}

type trafficBucket struct {
	start      int64
	conns      int
	ok         int
	dialFailed int
	aborted    int
	down, up   int64
}

const trafficBuckets = 60

func newTrafficStore(window time.Duration) *trafficStore {
	return &trafficStore{
		width:       max(window/trafficBuckets, time.Second),
		size:        trafficBuckets,
		nodes:       make(map[string][]trafficBucket),
		lastFailure: make(map[string]time.Time),
	}
}

func (t *trafficStore) bucket(node string, now time.Time) *trafficBucket {
	buckets := t.nodes[node]
	if buckets == nil {
		buckets = make([]trafficBucket, t.size)
		t.nodes[node] = buckets
	}
	start := now.UnixNano() / int64(t.width)
	bucket := &buckets[start%int64(t.size)]
	if bucket.start != start {
		*bucket = trafficBucket{start: start}
	}
	return bucket
}

func (t *trafficStore) started(node string, now time.Time) {
	t.access.Lock()
	defer t.access.Unlock()
	t.bucket(node, now).conns++
}

func (t *trafficStore) dialFailed(node string, now time.Time) {
	t.access.Lock()
	defer t.access.Unlock()
	bucket := t.bucket(node, now)
	bucket.conns++
	bucket.dialFailed++
	t.lastFailure[node] = now
}

func (t *trafficStore) ended(node string, failed bool, down, up int64, now time.Time) {
	t.access.Lock()
	defer t.access.Unlock()
	bucket := t.bucket(node, now)
	if failed {
		bucket.aborted++
		t.lastFailure[node] = now
	} else {
		bucket.ok++
	}
	bucket.down += down
	bucket.up += up
}

func (t *trafficStore) stats(node string, now time.Time) TrafficStats {
	t.access.Lock()
	defer t.access.Unlock()
	stats := TrafficStats{LastFailureAge: math.Inf(1)}
	if last, loaded := t.lastFailure[node]; loaded {
		stats.LastFailureAge = now.Sub(last).Seconds()
	}
	oldest := now.UnixNano()/int64(t.width) - int64(t.size) + 1
	for _, bucket := range t.nodes[node] {
		if bucket.start < oldest {
			continue
		}
		stats.Conns += bucket.conns
		stats.OK += bucket.ok
		stats.DialFailed += bucket.dialFailed
		stats.Aborted += bucket.aborted
		stats.BytesDown += bucket.down
		stats.BytesUp += bucket.up
	}
	if stats.Conns > 0 {
		stats.FailRate = min(float64(stats.DialFailed+stats.Aborted)/float64(stats.Conns), 1)
	}
	return stats
}

func (t *trafficStore) window() time.Duration {
	return t.width * time.Duration(t.size)
}

func (t *trafficStore) prune(live map[string]bool) {
	t.access.Lock()
	defer t.access.Unlock()
	for node := range t.nodes {
		if !live[node] {
			delete(t.nodes, node)
			delete(t.lastFailure, node)
		}
	}
}
