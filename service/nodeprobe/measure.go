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
// request to the response headers; transfer runs from the headers to the last
// byte read. A plain request has no transfer.
type sample struct {
	firstByte time.Duration
	transfer  time.Duration
	bytes     uint32
	err       error
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
	headers := time.Now()
	result := sample{firstByte: headers.Sub(start)}
	if size == 0 {
		return result
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return sample{err: E.New("HTTP ", response.StatusCode)}
	}
	var (
		total  uint32
		buffer = make([]byte, 32*1024)
	)
	for total < size {
		n, readErr := response.Body.Read(buffer[:min(uint32(len(buffer)), size-total)])
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
	result.transfer = time.Since(headers)
	result.bytes = total
	return result
}
