// Package admin is the admin API: everything the operator's UI calls, behind
// a session that only the admin password or a single-use sign-in link opens.
package admin

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"claudication/internal/api"
	"claudication/internal/config"
	"claudication/internal/httpapi/httpx"
	"claudication/internal/oauth"
	"claudication/internal/pool"
	"claudication/internal/provider"
	accountsvc "claudication/internal/service/accounts"
	"claudication/internal/service/clients"
	"claudication/internal/service/install"
	"claudication/internal/service/limits"
	modelsvc "claudication/internal/service/models"
	"claudication/internal/service/settings"
	"claudication/internal/store"
)

// Admin serves the admin API: sign-in and the session it opens, the accounts
// and keys, the usage screens, and the switches an operator flips at runtime.
type Admin struct {
	cfg            *config.Config
	log            *slog.Logger
	store          *store.Store
	pool           *pool.Pool
	pending        *oauth.Pending
	poller         *accountsvc.Poller
	images         *settings.Switch
	serving        *settings.Switch
	inflight       *limits.InFlight
	protocols      api.Registry
	httpClient     *http.Client
	providers      provider.Registry
	anonLimiter    *limits.Limiter
	budgets        *limits.Budgets
	trustedProxies []*net.IPNet
	startedAt      time.Time
	sessions       *sessions
	models         Models
	install        *install.Links
	facets         facetCache
}

// Deps is what an Admin is built from. Every field is required.
type Deps struct {
	Config  *config.Config
	Log     *slog.Logger
	Store   *store.Store
	Pool    *pool.Pool
	Pending *oauth.Pending
	Poller  *accountsvc.Poller
	Images  *settings.Switch
	// Serving is the gateway switch.
	Serving *settings.Switch
	// InFlight counts each key's open requests.
	InFlight *limits.InFlight
	Protocols  api.Registry
	HTTPClient *http.Client
	// Providers are the upstreams accounts are connected to, which the
	// account screens reach through rather than by name: signing in,
	// refreshing, testing and revoking are each the provider's own.
	Providers      provider.Registry
	AnonLimiter    *limits.Limiter
	Budgets        *limits.Budgets
	TrustedProxies []*net.IPNet
	StartedAt      time.Time
	// Models is the gateway's model list, which client names are bound into.
	Models Models
	// Install keeps the one-time install bundles the relay serves.
	Install *install.Links
}

// Models is what the admin screens read of the gateway's model list: the same
// answers /v1/models gives, so a screen says what a request would get.
type Models interface {
	// List is every model the gateway serves.
	List(ctx context.Context) ([]byte, error)
	// ListFor is the upstream list as one account sees it, unfiltered.
	ListFor(ctx context.Context, accountID, rawQuery string) ([]byte, error)
	// For is the list one key is offered, its client's slots in it: what
	// /v1/models answers it, and what its client's configs are written from.
	For(ctx context.Context, key store.APIKey) ([]byte, error)
	// Choices are what a slot of a client on a dialect can be bound to now,
	// and nothing that is not available to it.
	Choices(ctx context.Context, d clients.Dialect) []modelsvc.Choice
	// Available are the real models a client on a dialect can be served now.
	Available(ctx context.Context, d clients.Dialect) []modelsvc.Choice
	// Resolve is what a binding serves a client on a dialect now, and why it
	// is unavailable when it is.
	Resolve(ctx context.Context, d clients.Dialect, b store.Binding) (modelsvc.Choice, string)
}

// New builds an Admin with no session open.
func New(d Deps) *Admin {
	return &Admin{
		cfg: d.Config, log: d.Log, store: d.Store, pool: d.Pool,
		pending: d.Pending, poller: d.Poller, images: d.Images,
		serving: d.Serving, inflight: d.InFlight, protocols: d.Protocols,
		httpClient: d.HTTPClient, providers: d.Providers, anonLimiter: d.AnonLimiter, budgets: d.Budgets,
		trustedProxies: d.TrustedProxies, startedAt: d.StartedAt,
		sessions: newSessions(), models: d.Models, install: d.Install,
	}
}

// Routes registers the admin API on mux.
func (s *Admin) Routes(mux *http.ServeMux) {
	// Admin API. Setup and sign-in are outside RequireAdmin: a gateway with no
	// password yet has nothing to authenticate against.
	//
	// These two change state without a cookie to protect them, so they carry
	// their own cross-site check — see httpx.SameSiteOnly. Everything under
	// RequireAdmin is already covered by the session cookie being
	// SameSite=Strict.
	mux.HandleFunc("GET /admin/setup", s.handleSetupStatus)
	mux.Handle("POST /admin/setup", httpx.SameSiteOnly(http.HandlerFunc(s.handleSetup)))
	mux.Handle("POST /admin/session", httpx.SameSiteOnly(http.HandlerFunc(s.handleAdminLogin)))
	mux.HandleFunc("DELETE /admin/session", s.handleAdminLogout)
	mux.HandleFunc("GET /admin/session", s.handleTokenSession)

	admin := func(h http.HandlerFunc) http.Handler { return s.RequireAdmin(h) }
	mux.Handle("GET /admin/me", admin(s.handleAdminMe))
	mux.Handle("POST /admin/password", admin(s.handleChangePassword))
	mux.Handle("POST /admin/account/delete", admin(s.handleDeleteAdminAccount))
	mux.Handle("GET /admin/accounts", admin(s.handleListAccounts))
	mux.Handle("POST /admin/accounts/order", admin(s.handleReorderAccounts))
	mux.Handle("POST /admin/accounts/oauth/start", admin(s.handleOAuthStart))
	mux.Handle("POST /admin/accounts/oauth/complete", admin(s.handleOAuthComplete))
	mux.Handle("POST /admin/accounts/oauth/poll", admin(s.handleOAuthPoll))
	mux.Handle("POST /admin/accounts/{id}/test", admin(s.handleTestAccount))
	mux.Handle("POST /admin/accounts/{id}/refresh", admin(s.handleRefreshAccount))
	mux.Handle("POST /admin/accounts/{id}/usage", admin(s.handleRefreshUsage))
	mux.Handle("POST /admin/accounts/{id}/disabled", admin(s.handleSetAccountDisabled))
	mux.Handle("GET /admin/accounts/{id}/models", admin(s.handleAccountModels))
	mux.Handle("POST /admin/accounts/{id}/models", admin(s.handleSetAccountModel))
	mux.Handle("GET /admin/accounts/{id}/clients", admin(s.handleAccountClients))
	mux.Handle("DELETE /admin/accounts/{id}", admin(s.handleDeleteAccount))

	// Clients, each holding one key. The key's own endpoints, under the
	// client's id, are the credential's: its secret, its limits, its models,
	// its install bundle, deleting it.
	mux.Handle("GET /admin/client-types", admin(s.handleClientTypes))
	mux.Handle("GET /admin/clients", admin(s.handleListClients))
	mux.Handle("POST /admin/clients", admin(s.handleCreateClient))
	mux.Handle("GET /admin/clients/{id}", admin(s.handleGetClient))
	mux.Handle("POST /admin/clients/{id}/mode", admin(s.handleSetClientMode))
	mux.Handle("POST /admin/clients/{id}/disabled", admin(s.handleSetClientDisabled))
	mux.Handle("POST /admin/clients/{id}/name-chats", admin(s.handleSetClientNameChats))
	mux.Handle("POST /admin/clients/{id}/slots", admin(s.handleAddSlot))
	mux.Handle("PUT /admin/clients/{id}/slots/{slot}", admin(s.handleSetSlot))
	mux.Handle("DELETE /admin/clients/{id}/slots/{slot}", admin(s.handleRemoveSlot))
	mux.Handle("GET /admin/clients/{id}/usage", admin(s.handleClientUsage))
	mux.Handle("GET /admin/clients/{id}/limits", admin(s.handleClientLimits))
	mux.Handle("GET /admin/clients/{id}/limits/suggest", admin(s.handleClientSuggest))
	mux.Handle("POST /admin/clients/{id}/limits/preview", admin(s.handleClientPreview))

	mux.Handle("PATCH /admin/keys/{id}", admin(s.handleUpdateKey))
	mux.Handle("DELETE /admin/keys/{id}", admin(s.handleDeleteKey))
	mux.Handle("POST /admin/keys/{id}/rotate", admin(s.handleRotateKey))
	mux.Handle("POST /admin/keys/{id}/install", admin(s.handleKeyInstall))
	mux.Handle("GET /admin/keys/{id}/secret", admin(s.handleKeySecret))
	mux.Handle("GET /admin/keys/{id}/models", admin(s.handleKeyModels))
	mux.Handle("POST /admin/keys/{id}/models", admin(s.handleSetKeyModel))
	mux.Handle("GET /admin/keys/{id}/offered-models", admin(s.handleKeyOfferedModels))

	mux.Handle("GET /admin/config", admin(s.handleConfig))
	mux.Handle("GET /admin/models", admin(s.handleAdminModels))
	mux.Handle("POST /admin/gateway", admin(s.switchHandler(s.serving, "gateway", "gateway_enabled")))

	mux.Handle("GET /admin/overview", admin(s.handleOverview))
	mux.Handle("GET /admin/usage", admin(s.handleUsage))
	mux.Handle("GET /admin/chats", admin(s.handleChats))
	mux.Handle("GET /admin/chats/{id}", admin(s.handleChat))
	mux.Handle("PUT /admin/chats/{id}/title", admin(s.handleSetChatTitle))
	mux.Handle("POST /admin/fit-images", admin(s.switchHandler(s.images, "image fitting", "fit_images")))
	mux.Handle("GET /admin/requests", admin(s.handleRecentRequests))
	mux.Handle("GET /admin/requests/facets", admin(s.handleRequestFacets))
	mux.Handle("GET /admin/requests/export", admin(s.handleExportRequests))
}
