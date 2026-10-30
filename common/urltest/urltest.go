package urltest

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/sagernet/sing-anytls"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-mux"
	"github.com/sagernet/sing-snell"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/common/observable"
)

const DefaultURL = "https://www.gstatic.com/generate_204"

func NormalizeURL(link string) string {
	if link == "" {
		return DefaultURL
	}
	return link
}

type HistoryKey struct {
	OutboundTag string
	URL         string
}

type HistoryStorage struct {
	access        sync.RWMutex
	delayHistory  map[HistoryKey]*adapter.URLTestHistory
	latestHistory map[string]HistoryKey
	generations   map[string]uint64
	updateHooks   []*observable.Subscriber[struct{}]
}

func NewHistoryStorage() *HistoryStorage {
	return &HistoryStorage{
		delayHistory:  make(map[HistoryKey]*adapter.URLTestHistory),
		latestHistory: make(map[string]HistoryKey),
		generations:   make(map[string]uint64),
	}
}

func (s *HistoryStorage) AddUpdateHook(hook *observable.Subscriber[struct{}]) {
	s.access.Lock()
	defer s.access.Unlock()
	s.updateHooks = append(s.updateHooks, hook)
}

func (s *HistoryStorage) NotifyUpdated() {
	s.access.RLock()
	defer s.access.RUnlock()
	s.notifyUpdated()
}

func (s *HistoryStorage) LoadURLTestHistory(tag, link string) *adapter.URLTestHistory {
	if s == nil {
		return nil
	}
	s.access.RLock()
	defer s.access.RUnlock()
	return s.delayHistory[HistoryKey{OutboundTag: tag, URL: NormalizeURL(link)}]
}

// LoadLatestURLTestHistory returns the latest successful history for display only.
// Target-specific selection must use LoadURLTestHistory instead.
func (s *HistoryStorage) LoadLatestURLTestHistory(tag string) *adapter.URLTestHistory {
	if s == nil {
		return nil
	}
	s.access.RLock()
	defer s.access.RUnlock()
	return s.delayHistory[s.latestHistory[tag]]
}

// OutboundGeneration must be captured before starting an asynchronous probe.
func (s *HistoryStorage) OutboundGeneration(tag string) uint64 {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.generations[tag]
}

func (s *HistoryStorage) DeleteURLTestHistory(tag, link string, generation uint64) {
	key := HistoryKey{OutboundTag: tag, URL: NormalizeURL(link)}
	s.access.Lock()
	if s.generations[tag] != generation {
		s.access.Unlock()
		return
	}
	delete(s.delayHistory, key)
	if latestKey, loaded := s.latestHistory[tag]; loaded && latestKey == key {
		s.refreshLatestHistory(tag)
	}
	s.notifyUpdated()
	s.access.Unlock()
}

func (s *HistoryStorage) DeleteOutboundURLTestHistory(tag string) {
	s.access.Lock()
	s.generations[tag]++
	for key := range s.delayHistory {
		if key.OutboundTag == tag {
			delete(s.delayHistory, key)
		}
	}
	delete(s.latestHistory, tag)
	s.notifyUpdated()
	s.access.Unlock()
}

func (s *HistoryStorage) StoreURLTestHistory(tag, link string, generation uint64, history *adapter.URLTestHistory) {
	key := HistoryKey{OutboundTag: tag, URL: NormalizeURL(link)}
	s.access.Lock()
	if s.generations[tag] != generation {
		s.access.Unlock()
		return
	}
	previous := s.delayHistory[key]
	s.delayHistory[key] = history
	latestKey, loaded := s.latestHistory[tag]
	switch {
	case !loaded:
		if history != nil {
			s.latestHistory[tag] = key
		}
	case latestKey == key:
		// Replacing the newest result with an older one may expose another target.
		if history == nil || history.Time.Before(previous.Time) {
			s.refreshLatestHistory(tag)
		}
	case history != nil && history.Time.After(s.delayHistory[latestKey].Time):
		s.latestHistory[tag] = key
	}
	s.notifyUpdated()
	s.access.Unlock()
}

// refreshLatestHistory is called with access locked after invalidating the latest
// result. Ordinary loads and stores use the index without scanning or copying.
func (s *HistoryStorage) refreshLatestHistory(tag string) {
	var latestKey HistoryKey
	var latest *adapter.URLTestHistory
	for key, history := range s.delayHistory {
		if key.OutboundTag != tag || history == nil {
			continue
		}
		if latest == nil || history.Time.After(latest.Time) || history.Time.Equal(latest.Time) && key.URL < latestKey.URL {
			latestKey = key
			latest = history
		}
	}
	if latest == nil {
		delete(s.latestHistory, tag)
	} else {
		s.latestHistory[tag] = latestKey
	}
}

func (s *HistoryStorage) notifyUpdated() {
	for _, updateHook := range s.updateHooks {
		updateHook.Emit(struct{}{})
	}
}

func (s *HistoryStorage) Close() error {
	s.access.Lock()
	defer s.access.Unlock()
	s.updateHooks = nil
	return nil
}

func URLTest(ctx context.Context, link string, detour N.Dialer) (uint16, error) {
	multiplexOutbound, isMultiplexOutbound := common.Cast[adapter.OutboundWithMultiplex](detour)
	if isMultiplexOutbound && multiplexOutbound.MultiplexEnabled() {
		warmContext := adapter.ContextWithKeepSession(ctx)
		warmContext = mux.ContextWithKeepSession(warmContext)
		warmContext = anytls.ContextWithKeepSession(warmContext)
		warmContext = contextWithQUICKeepSession(warmContext)
		warmContext = snell.ContextWithKeepSession(warmContext)
		_, err := urlTest(warmContext, link, detour)
		if err != nil {
			return 0, err
		}
	}
	return urlTest(ctx, link, detour)
}

func urlTest(ctx context.Context, link string, detour N.Dialer) (t uint16, err error) {
	link = NormalizeURL(link)
	linkURL, err := url.Parse(link)
	if err != nil {
		return
	}
	hostname := linkURL.Hostname()
	port := linkURL.Port()
	if port == "" {
		switch linkURL.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}

	start := time.Now()
	instance, err := detour.DialContext(ctx, "tcp", M.ParseSocksaddrHostPortStr(hostname, port))
	if err != nil {
		return
	}
	defer instance.Close()
	if N.NeedHandshakeForWrite(instance) {
		start = time.Now()
	}
	req, err := http.NewRequest(http.MethodHead, link, nil)
	if err != nil {
		return
	}
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return instance, nil
			},
			TLSClientConfig: &tls.Config{
				Time:    ntp.TimeFuncFromContext(ctx),
				RootCAs: adapter.RootPoolFromContext(ctx),
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Timeout: C.TCPTimeout,
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return
	}
	resp.Body.Close()
	t = uint16(time.Since(start) / time.Millisecond)
	return
}
