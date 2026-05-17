package hosts

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/common/rw"
	"github.com/sagernet/sing/service/filemanager"
)

const (
	hostsProviderTypeRemote = "remote"
	defaultUpdateInterval   = 24 * time.Hour
	minUpdateInterval       = time.Hour
	defaultMaxSize          = 16 * 1024 * 1024
)

type RemoteProvider struct {
	ctx            context.Context
	cancel         context.CancelFunc
	logger         log.ContextLogger
	tag            string
	url            string
	path           string
	userAgent      string
	updateInterval time.Duration
	required       bool
	maxSize        uint64
	httpClient     *http.Client
	lastEtag       string
	ticker         *time.Ticker
	updating       atomic.Bool
}

func NewRemoteProvider(ctx context.Context, logger log.ContextLogger, index int, options option.HostsProviderOptions) (*RemoteProvider, error) {
	providerType := options.Type
	if providerType == "" {
		providerType = hostsProviderTypeRemote
	}
	if providerType != hostsProviderTypeRemote {
		return nil, E.New("unknown hosts provider type: ", options.Type)
	}
	if options.URL == "" {
		return nil, E.New("hosts provider URL is required")
	}
	if options.Path == "" {
		return nil, E.New("hosts provider path is required")
	}
	path := filemanager.BasePath(ctx, os.ExpandEnv(options.Path))
	path, _ = filepath.Abs(path)
	if rw.IsDir(path) {
		return nil, E.New("hosts provider path is a directory: ", path)
	}
	updateInterval := time.Duration(options.UpdateInterval)
	if updateInterval <= 0 {
		updateInterval = defaultUpdateInterval
	}
	if updateInterval < minUpdateInterval {
		updateInterval = minUpdateInterval
	}
	userAgent := options.UserAgent
	if userAgent == "" {
		userAgent = "sing-box " + C.Version
	}
	maxSize := uint64(defaultMaxSize)
	if options.MaxSize != nil && options.MaxSize.Value() > 0 {
		maxSize = options.MaxSize.Value()
	}
	tag := options.Tag
	if tag == "" {
		tag = F.ToString(index)
	}
	providerCtx, cancel := context.WithCancel(ctx)
	return &RemoteProvider{
		ctx:            providerCtx,
		cancel:         cancel,
		logger:         logger,
		tag:            tag,
		url:            options.URL,
		path:           path,
		userAgent:      userAgent,
		updateInterval: updateInterval,
		required:       options.Required,
		maxSize:        maxSize,
	}, nil
}

func (p *RemoteProvider) Path() string {
	return p.path
}

func (p *RemoteProvider) Start() error {
	httpClient, err := p.resolveHTTPClient()
	if err != nil {
		if p.required && !p.hasCacheFile() {
			return E.Cause(err, "create hosts provider http client")
		}
		p.logger.Warn("create hosts provider[", p.tag, "] http client: ", err)
		return nil
	}
	p.httpClient = httpClient
	err = p.fetch(p.ctx, true)
	if err != nil {
		if p.required && !p.hasCacheFile() {
			return E.Cause(err, "initial hosts provider: ", p.tag)
		}
		p.logger.Warn("initial hosts provider[", p.tag, "]: ", err)
	}
	go p.loopUpdate()
	return nil
}

func (p *RemoteProvider) Close() error {
	p.cancel()
	if p.ticker != nil {
		p.ticker.Stop()
	}
	if p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	return nil
}

func (p *RemoteProvider) resolveHTTPClient() (*http.Client, error) {
	return &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: C.TCPTimeout,
		TLSClientConfig: &tls.Config{
			Time:    ntp.TimeFuncFromContext(p.ctx),
			RootCAs: adapter.RootPoolFromContext(p.ctx),
		},
	}}, nil
}

func (p *RemoteProvider) updateOnce() {
	if err := p.fetch(p.ctx, false); err != nil {
		p.logger.Error("update hosts provider[", p.tag, "]: ", err)
	}
}

func (p *RemoteProvider) fetch(ctx context.Context, isStart bool) error {
	if p.httpClient == nil {
		return E.New("http client is not initialized")
	}
	if p.updating.Swap(true) {
		return E.New("hosts provider is updating")
	}
	defer p.updating.Store(false)
	p.logger.Debug("updating hosts provider[", p.tag, "] from URL: ", p.url)
	request, err := http.NewRequest(http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}
	if p.lastEtag != "" {
		request.Header.Set("If-None-Match", p.lastEtag)
	}
	request.Header.Set("User-Agent", p.userAgent)
	if !isStart {
		defer p.httpClient.CloseIdleConnections()
	}
	response, err := p.httpClient.Do(request.WithContext(ctx))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotModified:
		p.logger.Info("update hosts provider[", p.tag, "]: not modified")
		return nil
	default:
		return E.New("unexpected status: ", response.Status)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, int64(p.maxSize)+1))
	if err != nil {
		return err
	}
	if uint64(len(content)) > p.maxSize {
		return E.New("hosts provider content exceeds max_size: ", p.maxSize)
	}
	if err = p.writeCacheFile(content); err != nil {
		return err
	}
	if eTag := response.Header.Get("Etag"); eTag != "" {
		p.lastEtag = eTag
	}
	p.logger.Info("updated hosts provider[", p.tag, "]")
	return nil
}

func (p *RemoteProvider) writeCacheFile(content []byte) error {
	dir := filepath.Dir(p.path)
	if err := filemanager.MkdirAll(p.ctx, dir, 0o755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(p.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	_, writeErr := tmpFile.Write(content)
	closeErr := tmpFile.Close()
	if writeErr != nil {
		os.Remove(tmpPath)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return closeErr
	}
	if err = os.Chmod(tmpPath, 0o666); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err = os.Rename(tmpPath, p.path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func (p *RemoteProvider) hasCacheFile() bool {
	stat, err := os.Stat(p.path)
	return err == nil && !stat.IsDir() && stat.Size() > 0
}

func (p *RemoteProvider) loopUpdate() {
	p.ticker = time.NewTicker(p.updateInterval)
	defer p.ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.ticker.C:
			p.updateOnce()
		}
	}
}
