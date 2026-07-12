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
	Resolver      UserResolver
	Tokens        TokenSink // optional: caches Graph tokens for background jobs
	Orders        *OrdersController
	Subscriptions *SubscriptionsController
	Discrepancies *DiscrepanciesController
	Otto          *OttoController
	Checkups      *CheckupsController
	Webhooks      *WebhooksController
	WS            WSHandler
	Log           *slog.Logger
}

// NewRouter assembles the Echo app. E2E tests build the same router with
// faked gateways/repos behind the real usecases and middleware.
func NewRouter(d RouterDeps) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	if d.Log != nil {
		e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
			LogStatus: true, LogURI: true, LogMethod: true,
			LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
				d.Log.Info("request", "method", v.Method, "uri", v.URI, "status", v.Status)
				return nil
			},
		}))
	}

	auth := AuthMiddleware(d.Resolver, d.Tokens)

	e.GET("/orders", d.Orders.GetOrders, auth)
	e.POST("/sync", d.Orders.PostSync, auth)
	e.GET("/ws", func(c echo.Context) error { return d.WS.Handler(c) }, auth)
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
