package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/daemon"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

func TestProviderServiceGRPCWebAuthentication(t *testing.T) {
	server, handler := newProviderTransportTestServer(t)
	request := func(authorization string) grpcWebResponse {
		t.Helper()
		httpRequest, err := http.NewRequest(
			http.MethodPost,
			server.URL+daemon.ProviderService_GetProviderServiceInfo_FullMethodName,
			bytes.NewReader(grpcDataFrame(nil)),
		)
		if err != nil {
			t.Fatal(err)
		}
		httpRequest.Header.Set("Content-Type", "application/grpc-web+proto")
		if authorization != "" {
			httpRequest.Header.Set("Authorization", authorization)
		}
		response, err := handler.Do(httpRequest)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return grpcWebResponse{header: response.Header, frames: parseGRPCWebFrames(t, body)}
	}

	allowed := request("Bearer test-secret")
	var info daemon.ProviderServiceInfo
	data := firstGRPCWebData(allowed.frames)
	if data == nil || proto.Unmarshal(data, &info) != nil || info.ProtocolVersion != daemon.ProviderServiceProtocolVersion || grpcWebStatus(allowed) != "0" {
		t.Fatalf("unexpected authenticated gRPC-Web response: %+v", allowed)
	}
	denied := request("")
	if grpcWebStatus(denied) != "16" {
		t.Fatalf("unexpected unauthenticated gRPC-Web response: %+v", denied)
	}
}

func TestProviderServiceWebSocketAuthentication(t *testing.T) {
	server, _ := newProviderTransportTestServer(t)
	webSocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + daemon.ProviderService_GetProviderServiceInfo_FullMethodName
	request := func(authorization string) grpcWebResponse {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		connection, _, err := websocket.Dial(ctx, webSocketURL, &websocket.DialOptions{Subprotocols: []string{webSocketSubprotocol}})
		if err != nil {
			t.Fatal(err)
		}
		defer connection.CloseNow()
		header := "content-type: application/grpc-web+proto\r\n"
		if authorization != "" {
			header += "authorization: " + authorization + "\r\n"
		}
		if err := connection.Write(ctx, websocket.MessageBinary, []byte(header)); err != nil {
			t.Fatal(err)
		}
		if err := connection.Write(ctx, websocket.MessageBinary, append([]byte{0}, grpcDataFrame(nil)...)); err != nil {
			t.Fatal(err)
		}
		if err := connection.Write(ctx, websocket.MessageBinary, []byte{1}); err != nil {
			t.Fatal(err)
		}
		var content []byte
		for {
			_, message, readErr := connection.Read(ctx)
			if readErr != nil {
				break
			}
			content = append(content, message...)
		}
		return grpcWebResponse{frames: parseGRPCWebFrames(t, content)}
	}

	allowed := request("Bearer test-secret")
	var info daemon.ProviderServiceInfo
	data := firstGRPCWebData(allowed.frames)
	if data == nil || proto.Unmarshal(data, &info) != nil || info.ProtocolVersion != daemon.ProviderServiceProtocolVersion || grpcWebStatus(allowed) != "0" {
		t.Fatalf("unexpected authenticated WebSocket response: %+v", allowed)
	}
	denied := request("")
	if grpcWebStatus(denied) != "16" {
		t.Fatalf("unexpected unauthenticated WebSocket response: %+v", denied)
	}
}

func newProviderTransportTestServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	startedService := daemon.NewStartedService(daemon.ServiceOptions{Context: ctx})
	grpcServer := daemon.NewServer(startedService, "test-secret")
	httpServer := httptest.NewServer(newHTTPHandler(log.StdLogger(), grpcServer, option.APIServiceOptions{}, nil))
	t.Cleanup(func() {
		httpServer.Close()
		grpcServer.Stop()
		startedService.Close()
		cancel()
	})
	return httpServer, httpServer.Client()
}

type grpcWebFrame struct {
	trailer bool
	payload []byte
}

type grpcWebResponse struct {
	header http.Header
	frames []grpcWebFrame
}

func firstGRPCWebData(frames []grpcWebFrame) []byte {
	for _, frame := range frames {
		if !frame.trailer {
			return frame.payload
		}
	}
	return nil
}

func grpcWebStatus(response grpcWebResponse) string {
	if value := response.header.Get("Grpc-Status"); value != "" {
		return value
	}
	for _, frame := range response.frames {
		if !frame.trailer {
			continue
		}
		for _, line := range strings.Split(string(frame.payload), "\r\n") {
			if value, found := strings.CutPrefix(strings.ToLower(line), "grpc-status: "); found {
				return value
			}
		}
	}
	return ""
}

func grpcDataFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func parseGRPCWebFrames(t *testing.T, content []byte) []grpcWebFrame {
	t.Helper()
	var frames []grpcWebFrame
	for len(content) > 0 {
		if len(content) < 5 {
			t.Fatalf("truncated gRPC-Web frame: %x", content)
		}
		length := int(binary.BigEndian.Uint32(content[1:5]))
		if len(content) < 5+length {
			t.Fatalf("truncated gRPC-Web payload: %x", content)
		}
		frames = append(frames, grpcWebFrame{trailer: content[0]&0x80 != 0, payload: append([]byte(nil), content[5:5+length]...)})
		content = content[5+length:]
	}
	return frames
}
