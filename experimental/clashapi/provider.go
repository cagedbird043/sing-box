package clashapi

import (
	"context"
	"net/http"
	"sort"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/json/badjson"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

func proxyProviderRouter(server *Server) http.Handler {
	r := chi.NewRouter()
	r.Get("/", getProviders(server))

	r.Route("/{name}", func(r chi.Router) {
		r.Use(parseProviderName, findProviderByName(server))
		r.Get("/", getProvider(server))
		r.Put("/", updateProvider)
		r.Get("/healthcheck", healthCheckProvider)
	})
	return r
}

func getProviders(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var providerMap badjson.JSONObject
		if server.provider != nil {
			providers := server.provider.Providers()
			sort.SliceStable(providers, func(i, j int) bool {
				return providers[i].Tag() < providers[j].Tag()
			})
			for _, provider := range providers {
				providerMap.Put(provider.Tag(), providerInfo(server, provider))
			}
		}
		var responseMap badjson.JSONObject
		responseMap.Put("providers", &providerMap)
		response, err := responseMap.MarshalJSON()
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		w.Write(response)
	}
}

func getProvider(server *Server) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := r.Context().Value(CtxKeyProvider).(adapter.Provider)
		response, err := providerInfo(server, provider).MarshalJSON()
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, newError(err.Error()))
			return
		}
		w.Write(response)
	}
}

func updateProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.Context().Value(CtxKeyProvider).(adapter.Provider)
	updater, ok := provider.(adapter.ProviderUpdater)
	if !ok {
		render.Status(r, http.StatusBadRequest)
		render.JSON(w, r, newError("provider does not support update"))
		return
	}
	if err := updater.Update(); err != nil {
		render.Status(r, http.StatusServiceUnavailable)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	render.NoContent(w, r)
}

func healthCheckProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.Context().Value(CtxKeyProvider).(adapter.Provider)
	result, err := provider.HealthCheck(r.Context())
	if err != nil {
		render.Status(r, http.StatusServiceUnavailable)
		render.JSON(w, r, newError(err.Error()))
		return
	}
	render.JSON(w, r, result)
}

func parseProviderName(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := getEscapeParam(r, "name")
		ctx := context.WithValue(r.Context(), CtxKeyProviderName, name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func findProviderByName(server *Server) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.Context().Value(CtxKeyProviderName).(string)
			if server.provider == nil {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}
			provider, exist := server.provider.Get(name)
			if !exist {
				render.Status(r, http.StatusNotFound)
				render.JSON(w, r, ErrNotFound)
				return
			}

			ctx := context.WithValue(r.Context(), CtxKeyProvider, provider)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func providerInfo(server *Server, provider adapter.Provider) *badjson.JSONObject {
	var info badjson.JSONObject
	info.Put("name", provider.Tag())
	info.Put("type", "Proxy")
	info.Put("vehicleType", providerVehicleType(provider.Type()))
	info.Put("updatedAt", provider.UpdatedAt())

	proxies := make([]*badjson.JSONObject, 0, len(provider.Outbounds()))
	for _, detour := range provider.Outbounds() {
		proxies = append(proxies, proxyInfo(server, detour))
	}
	info.Put("proxies", proxies)

	if subscription, hasSubscription := provider.(adapter.ProviderSubscriptionInfo); hasSubscription {
		info.Put("subscriptionInfo", providerSubscriptionInfo(subscription.SubscriptionInfo()))
	}
	return &info
}

func providerVehicleType(providerType string) string {
	switch providerType {
	case C.ProviderTypeRemote:
		return "HTTP"
	case C.ProviderTypeLocal:
		return "File"
	case C.ProviderTypeInline:
		return "Compatible"
	default:
		return C.ProviderDisplayName(providerType)
	}
}

func providerSubscriptionInfo(info adapter.SubscriptionInfo) render.M {
	return render.M{
		"Upload":   info.Upload,
		"Download": info.Download,
		"Total":    info.Total,
		"Expire":   info.Expire,
	}
}
