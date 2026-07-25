// Package http is the HTTP entrypoint ring: Echo router, middleware,
// controllers, and DTOs. Controllers call usecases; nothing calls back up.
package http

import (
	"log/slog"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// WSHandler is the realtime channel's Echo handler (controller/ws.Hub).
type WSHandler interface {
	Handler(c echo.Context) error
}

type RouterDeps struct {
	Resolver       UserResolver
	Tokens         TokenSink     // optional: caches Graph tokens for background jobs
	Auth           Authenticator // optional: aad mode; when set, Resolver/Tokens are unused
	AllowedOrigins []string  // CORS; empty means allow all (bearer auth, no cookies)
	Orders         *OrdersController
	Subscriptions  *SubscriptionsController
	Discrepancies  *DiscrepanciesController
	Otto           *OttoController
	Checkups       *CheckupsController
	Webhooks       *WebhooksController
	WS             WSHandler
	Log            *slog.Logger
}

// NewRouter assembles the Echo app. E2E tests build the same router with
// faked gateways/repos behind the real usecases and middleware.
func NewRouter(d RouterDeps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	// Browsers require CORS opt-in for cross-origin calls; safe with wildcard
	// because auth is a bearer header, never cookies. Empty AllowOrigins
	// falls back to Echo's default of "*".
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: d.AllowedOrigins,
		AllowHeaders: []string{echo.HeaderAuthorization, echo.HeaderContentType},
	}))
	if d.Log != nil {
		e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
			LogStatus: true, LogURI: true, LogMethod: true,
			LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
				uri := v.URI
				// /ws carries the auth token in its query string; never log it.
				if path := c.Request().URL.Path; path == "/ws" {
					uri = path
				}
				d.Log.Info("request", "method", v.Method, "uri", uri, "status", v.Status)
				return nil
			},
		}))
	}

	auth := AuthMiddleware(d.Resolver, d.Tokens)
	if d.Auth != nil {
		auth = AuthMiddlewareWith(d.Auth)
	}

	e.GET("/orders", d.Orders.GetOrders, auth)
	e.POST("/sync", d.Orders.PostSync, auth)
	// Browser WebSocket clients can't set headers, so /ws alone also accepts
	// ?token=, promoted into the Authorization header before the same auth
	// middleware every other route uses.
	e.GET("/ws", func(c echo.Context) error { return d.WS.Handler(c) }, wsQueryTokenFallback, auth)
	if d.Subscriptions != nil {
		e.POST("/subscriptions", d.Subscriptions.PostSubscriptions, auth)
	}
	if d.Discrepancies != nil {
		e.GET("/discrepancies", d.Discrepancies.GetDiscrepancies, auth)
		e.PATCH("/discrepancies/:discrepancyId", d.Discrepancies.PatchDiscrepancy, auth)
	}
	if d.Otto != nil {
		e.POST("/otto/scans", d.Otto.PostScans, auth)
		e.POST("/otto/scans/:scanId/record", d.Otto.PostRecord, auth)
	}
	if d.Checkups != nil {
		e.POST("/checkups", d.Checkups.PostCheckups, auth)
		e.GET("/checkups", d.Checkups.GetCheckups, auth)
		e.PATCH("/checkups/:checkupId", d.Checkups.PatchCheckup, auth)
	}
	if d.Webhooks != nil {
		// No auth middleware: Graph POSTs here directly; clientState is the check.
		e.POST("/webhooks/graph", d.Webhooks.PostGraph)
	}

	return e
}

// wsQueryTokenFallback promotes ?token= into the Authorization header when no
// header is present. Echo has already URL-decoded the query param. An existing
// header — valid or not — always wins; the query param is then ignored.
func wsQueryTokenFallback(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Header.Get("Authorization") == "" {
			if token := c.QueryParam("token"); token != "" {
				c.Request().Header.Set("Authorization", "Bearer "+token)
			}
		}
		return next(c)
	}
}
