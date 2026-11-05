package nodeprobe

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

// sample is one request through one outbound. firstByte runs from sending the
// request to the first body byte (or to the response headers for a plain
// request); transfer runs from the first byte to the last byte read.
type sample struct {
	firstByte time.Duration
	transfer  time.Duration
	err       error
}

// junk reports whether a download sample disqualifies its outbound: it failed,
// or reading the body took more than maxRatio times as long as reaching the first
// byte. The ratio, not absolute speed, is judged because the first byte already
// pays the handshake round trips, so a slow but healthy far node scales both
// sides alike while a throttled node only stretches the transfer.
func (s sample) junk(maxRatio float64) bool {
	if s.err != nil {
		return true
	}
	return float64(s.transfer) > maxRatio*float64(s.firstByte)
}

func (s sample) ratio() float64 {
	if s.firstByte <= 0 {
		return 0
	}
	return float64(s.transfer) / float64(s.firstByte)
}

// fetch requests link through detour. With size > 0 it downloads the first size
// bytes and fails on a shorter body; with size == 0 any HTTP response counts.
func fetch(ctx context.Context, detour N.Dialer, link string, size uint32, timeout time.Duration) sample {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return detour.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(addr))
		},
		TLSClientConfig: &tls.Config{
			Time:    ntp.TimeFuncFromContext(ctx),
			RootCAs: adapter.RootPoolFromContext(ctx),
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return sample{err: err}
	}
	request.Header.Set("Accept-Encoding", "identity")
	if size > 0 {
		request.Header.Set("Range", "bytes=0-"+strconv.FormatUint(uint64(size)-1, 10))
	}
	start := time.Now()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return sample{err: err}
	}
	defer response.Body.Close()
	if size == 0 {
		return sample{firstByte: time.Since(start)}
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return sample{err: E.New("HTTP ", response.StatusCode)}
	}
	var (
		result sample
		first  time.Time
		total  uint32
		buffer = make([]byte, 32*1024)
	)
	for total < size {
		n, readErr := response.Body.Read(buffer[:min(uint32(len(buffer)), size-total)])
		if n > 0 && first.IsZero() {
			first = time.Now()
			result.firstByte = first.Sub(start)
		}
		total += uint32(n)
		if readErr != nil {
			if readErr == io.EOF && total >= size {
				break
			}
			if readErr == io.EOF {
				readErr = E.New("short body: ", total, " of ", size, " bytes")
			}
			return sample{err: readErr}
		}
	}
	result.transfer = time.Since(first)
	return result
}
