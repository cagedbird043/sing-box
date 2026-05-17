package clashapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

const (
	tailscaleStatusTimeout = 5 * time.Second
	tailscalePingTimeout   = 10 * time.Second
)

type tailscaleEndpoint struct {
	tag      string
	provider adapter.TailscaleEndpoint
}

type tailscaleStatusResponse struct {
	Endpoints []*tailscaleEndpointStatus `json:"endpoints"`
	Complete  bool                       `json:"complete"`
}

type tailscaleEndpointStatus struct {
	EndpointTag    string                `json:"endpointTag"`
	BackendState   string                `json:"backendState"`
	AuthURL        string                `json:"authURL,omitempty"`
	NetworkName    string                `json:"networkName,omitempty"`
	MagicDNSSuffix string                `json:"magicDNSSuffix,omitempty"`
	Self           *tailscalePeer        `json:"self,omitempty"`
	UserGroups     []*tailscaleUserGroup `json:"userGroups,omitempty"`
}

type tailscaleUserGroup struct {
	UserID        int64            `json:"userID"`
	LoginName     string           `json:"loginName,omitempty"`
	DisplayName   string           `json:"displayName,omitempty"`
	ProfilePicURL string           `json:"profilePicURL,omitempty"`
	Peers         []*tailscalePeer `json:"peers,omitempty"`
}

type tailscalePeer struct {
	HostName       string   `json:"hostName,omitempty"`
	DNSName        string   `json:"dnsName,omitempty"`
	OS             string   `json:"os,omitempty"`
	TailscaleIPs   []string `json:"tailscaleIPs,omitempty"`
	Online         bool     `json:"online"`
	ExitNode       bool     `json:"exitNode"`
	ExitNodeOption bool     `json:"exitNodeOption"`
	Active         bool     `json:"active"`
	RxBytes        int64    `json:"rxBytes,omitempty"`
	TxBytes        int64    `json:"txBytes,omitempty"`
	UserID         int64    `json:"userID,omitempty"`
	KeyExpiry      int64    `json:"keyExpiry,omitempty"`
}

type tailscalePingRequest struct {
	EndpointTag string `json:"endpointTag"`
	PeerIP      string `json:"peerIP"`
}

type tailscalePingResponse struct {
	LatencyMs      float64 `json:"latencyMs"`
	IsDirect       bool    `json:"isDirect"`
	Endpoint       string  `json:"endpoint,omitempty"`
	DERPRegionID   int32   `json:"derpRegionID,omitempty"`
	DERPRegionCode string  `json:"derpRegionCode,omitempty"`
	Error          string  `json:"error,omitempty"`
}

func tailscaleRouter(server *Server) http.Handler {
	r := chi.NewRouter()
	r.Get("/status", getTailscaleStatus(server))
	r.Post("/ping", startTailscalePing(server))
	return r
}

func getTailscaleStatus(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		endpoints := tailscaleEndpoints(server)
		if len(endpoints) == 0 {
			render.Status(r, http.StatusNotFound)
			render.JSON(w, r, newError("no Tailscale endpoint found"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), tailscaleStatusTimeout)
		defer cancel()

		type taggedStatus struct {
			tag    string
			status *adapter.TailscaleEndpointStatus
		}
		updates := make(chan taggedStatus, len(endpoints))
		errorsChan := make(chan error, len(endpoints))
		var waitGroup sync.WaitGroup
		for _, endpoint := range endpoints {
			waitGroup.Add(1)
			go func(endpoint tailscaleEndpoint) {
				defer waitGroup.Done()
				err := endpoint.provider.SubscribeTailscaleStatus(ctx, func(status *adapter.TailscaleEndpointStatus) {
					if status == nil {
						return
					}
					select {
					case updates <- taggedStatus{tag: endpoint.tag, status: status}:
					case <-ctx.Done():
					default:
					}
				})
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					select {
					case errorsChan <- err:
					case <-ctx.Done():
					default:
					}
				}
			}(endpoint)
		}
		done := make(chan struct{})
		go func() {
			waitGroup.Wait()
			close(done)
		}()

		statuses := make(map[string]*adapter.TailscaleEndpointStatus, len(endpoints))
		var order []string
		var firstErr error
		complete := false
		for !complete {
			select {
			case update := <-updates:
				if update.status == nil {
					continue
				}
				if _, exists := statuses[update.tag]; !exists {
					order = append(order, update.tag)
				}
				statuses[update.tag] = update.status
				complete = len(statuses) == len(endpoints)
			case err := <-errorsChan:
				if firstErr == nil {
					firstErr = err
				}
			case <-done:
				complete = len(statuses) == len(endpoints)
				if !complete && len(statuses) > 0 {
					complete = true
				}
				if !complete {
					if firstErr != nil {
						render.Status(r, http.StatusServiceUnavailable)
						render.JSON(w, r, newError(firstErr.Error()))
						return
					}
					render.Status(r, http.StatusServiceUnavailable)
					render.JSON(w, r, newError("Tailscale status unavailable"))
					return
				}
			case <-ctx.Done():
				if len(statuses) == 0 {
					render.Status(r, http.StatusGatewayTimeout)
					render.JSON(w, r, newError(ctx.Err().Error()))
					return
				}
				complete = true
			}
		}
		cancel()

		responseEndpoints := make([]*tailscaleEndpointStatus, 0, len(order))
		for _, tag := range order {
			responseEndpoints = append(responseEndpoints, tailscaleStatusToJSON(tag, statuses[tag]))
		}
		render.JSON(w, r, &tailscaleStatusResponse{
			Endpoints: responseEndpoints,
			Complete:  len(statuses) == len(endpoints),
		})
	}
}

func startTailscalePing(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var request tailscalePingRequest
		if err := render.DecodeJSON(r.Body, &request); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, ErrBadRequest)
			return
		}
		if request.PeerIP == "" {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, newError("peerIP is required"))
			return
		}

		provider, err := tailscaleEndpointByTag(server, request.EndpointTag)
		if err != nil {
			if errors.Is(err, errTailscaleEndpointNotFound) {
				render.Status(r, http.StatusNotFound)
			} else {
				render.Status(r, http.StatusBadRequest)
			}
			render.JSON(w, r, newError(err.Error()))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), tailscalePingTimeout)
		defer cancel()

		results := make(chan *adapter.TailscalePingResult, 1)
		errorsChan := make(chan error, 1)
		go func() {
			err := provider.StartTailscalePing(ctx, request.PeerIP, func(result *adapter.TailscalePingResult) {
				if result == nil {
					return
				}
				select {
				case results <- result:
				case <-ctx.Done():
				default:
				}
			})
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				select {
				case errorsChan <- err:
				case <-ctx.Done():
				default:
				}
			}
		}()

		select {
		case result := <-results:
			cancel()
			render.JSON(w, r, tailscalePingResultToJSON(result))
		case err := <-errorsChan:
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, newError(err.Error()))
		case <-ctx.Done():
			render.Status(r, http.StatusGatewayTimeout)
			render.JSON(w, r, newError(ctx.Err().Error()))
		}
	}
}

var errTailscaleEndpointNotFound = errors.New("no Tailscale endpoint found")

func tailscaleEndpoints(server *Server) []tailscaleEndpoint {
	if server.endpoint == nil {
		return nil
	}
	var endpoints []tailscaleEndpoint
	for _, endpoint := range server.endpoint.Endpoints() {
		if endpoint.Type() != C.TypeTailscale {
			continue
		}
		provider, loaded := endpoint.(adapter.TailscaleEndpoint)
		if !loaded {
			continue
		}
		endpoints = append(endpoints, tailscaleEndpoint{
			tag:      endpoint.Tag(),
			provider: provider,
		})
	}
	return endpoints
}

func tailscaleEndpointByTag(server *Server, tag string) (adapter.TailscaleEndpoint, error) {
	if server.endpoint == nil {
		return nil, errTailscaleEndpointNotFound
	}
	if tag == "" {
		for _, endpoint := range tailscaleEndpoints(server) {
			return endpoint.provider, nil
		}
		return nil, errTailscaleEndpointNotFound
	}
	endpoint, loaded := server.endpoint.Get(tag)
	if !loaded {
		return nil, fmt.Errorf("%w: %s", errTailscaleEndpointNotFound, tag)
	}
	if endpoint.Type() != C.TypeTailscale {
		return nil, errors.New("endpoint is not Tailscale: " + tag)
	}
	provider, loaded := endpoint.(adapter.TailscaleEndpoint)
	if !loaded {
		return nil, errors.New("endpoint does not support Tailscale status or ping: " + tag)
	}
	return provider, nil
}

func tailscaleStatusToJSON(tag string, status *adapter.TailscaleEndpointStatus) *tailscaleEndpointStatus {
	result := &tailscaleEndpointStatus{
		EndpointTag:    tag,
		BackendState:   status.BackendState,
		AuthURL:        status.AuthURL,
		NetworkName:    status.NetworkName,
		MagicDNSSuffix: status.MagicDNSSuffix,
		UserGroups:     make([]*tailscaleUserGroup, 0, len(status.UserGroups)),
	}
	if status.Self != nil {
		result.Self = tailscalePeerToJSON(status.Self)
	}
	for _, group := range status.UserGroups {
		peers := make([]*tailscalePeer, 0, len(group.Peers))
		for _, peer := range group.Peers {
			peers = append(peers, tailscalePeerToJSON(peer))
		}
		result.UserGroups = append(result.UserGroups, &tailscaleUserGroup{
			UserID:        group.UserID,
			LoginName:     group.LoginName,
			DisplayName:   group.DisplayName,
			ProfilePicURL: group.ProfilePicURL,
			Peers:         peers,
		})
	}
	return result
}

func tailscalePeerToJSON(peer *adapter.TailscalePeer) *tailscalePeer {
	return &tailscalePeer{
		HostName:       peer.HostName,
		DNSName:        peer.DNSName,
		OS:             peer.OS,
		TailscaleIPs:   peer.TailscaleIPs,
		Online:         peer.Online,
		ExitNode:       peer.ExitNode,
		ExitNodeOption: peer.ExitNodeOption,
		Active:         peer.Active,
		RxBytes:        peer.RxBytes,
		TxBytes:        peer.TxBytes,
		UserID:         peer.UserID,
		KeyExpiry:      peer.KeyExpiry,
	}
}

func tailscalePingResultToJSON(result *adapter.TailscalePingResult) *tailscalePingResponse {
	return &tailscalePingResponse{
		LatencyMs:      result.LatencyMs,
		IsDirect:       result.IsDirect,
		Endpoint:       result.Endpoint,
		DERPRegionID:   result.DERPRegionID,
		DERPRegionCode: result.DERPRegionCode,
		Error:          result.Error,
	}
}
