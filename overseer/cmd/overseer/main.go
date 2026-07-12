// Command overseer is the composition root: config, DB pool + migrations,
// wire gateways → usecases → controllers, start HTTP. Nothing else in the
// codebase knows how the layers are glued together.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/thirdeye/overseer"
	httpctl "github.com/thirdeye/overseer/internal/controller/http"
	"github.com/thirdeye/overseer/internal/controller/ws"
	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/gateway/drive"
	"github.com/thirdeye/overseer/internal/gateway/excel"
	"github.com/thirdeye/overseer/internal/gateway/graph"
	"github.com/thirdeye/overseer/internal/gateway/llm"
	"github.com/thirdeye/overseer/internal/repo"
	"github.com/thirdeye/overseer/internal/tokencache"
	"github.com/thirdeye/overseer/internal/usecase/checkups"
	"github.com/thirdeye/overseer/internal/usecase/messages"
	"github.com/thirdeye/overseer/internal/usecase/orders"
	"github.com/thirdeye/overseer/internal/usecase/otto"
	"github.com/thirdeye/overseer/internal/usecase/subscriptions"
)

// Compile-time conformance: adapters satisfy the usecase ports structurally,
// asserted here so a drifting signature fails the build, not a request.
var (
	_ orders.MessageLister   = (*graph.Client)(nil)
	_ orders.OrderLLM        = (*llm.Gateway)(nil)
	_ orders.OrderRepo       = (*repo.Orders)(nil)
	_ orders.EventPublisher  = (*ws.Hub)(nil)
	_ orders.ThreadFetcher   = (*graph.Client)(nil)
	_ orders.DiscrepancyLLM  = (*llm.Gateway)(nil)
	_ orders.DiscrepancyRepo = (*repo.Discrepancies)(nil)

	_ messages.MessageGetter        = (*graph.Client)(nil)
	_ messages.OrderLLM             = (*llm.Gateway)(nil)
	_ messages.OrderRepo            = (*repo.Orders)(nil)
	_ messages.SubscriptionResolver = (*repo.Subscriptions)(nil)
	_ messages.TokenSource          = (*tokencache.Cache)(nil)
	_ messages.EventPublisher       = (*ws.Hub)(nil)
	_ messages.DiscrepancyCatcher   = (*orders.CatchDiscrepancies)(nil)

	_ subscriptions.GraphSubscriber  = (*graph.Client)(nil)
	_ subscriptions.SubscriptionRepo = (*repo.Subscriptions)(nil)
	_ subscriptions.TokenSource      = (*tokencache.Cache)(nil)

	_ checkups.MailSender  = (*graph.Client)(nil)
	_ checkups.TokenSource = (*tokencache.Cache)(nil)
	_ checkups.CheckupRepo = (*repo.Checkups)(nil)

	_ messages.CheckupCanceller = (*repo.Checkups)(nil)

	_ otto.ThreadFetcher = (*graph.Client)(nil)
	_ otto.DriveSearcher = (*drive.Client)(nil)
	_ otto.ItemGetter    = (*drive.Client)(nil)
	_ otto.OttoLLM       = (*llm.Gateway)(nil)
	_ otto.PlanLLM       = (*llm.Gateway)(nil)
	_ otto.Workbook      = (*excel.Client)(nil)
	_ otto.ScanRepo      = (*repo.OttoScans)(nil)

	_ httpctl.TokenSink = (*tokencache.Cache)(nil)
)

type config struct {
	Port             string
	DatabaseURL      string
	LLMModel         string
	SeedAccountName  string
	SeedUserEmail    string
	SyncLookbackDays int

	PublicBaseURL    string // where Graph reaches us; must be public https in real use
	GraphClientState string // shared secret in every notification
	SubscriptionTTL  time.Duration
	RenewAhead       time.Duration
	RenewInterval    time.Duration
	CheckupInterval  time.Duration
}

func loadConfig() config {
	return config{
		Port:             envOr("PORT", "8080"),
		DatabaseURL:      envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/overseer?sslmode=disable"),
		LLMModel:         envOr("LLM_MODEL", llm.DefaultModel),
		SeedAccountName:  envOr("SEED_ACCOUNT_NAME", "ThirdEye Dev"),
		SeedUserEmail:    envOr("SEED_USER_EMAIL", "operator@example.com"),
		SyncLookbackDays: 30,

		PublicBaseURL:    envOr("PUBLIC_BASE_URL", "http://localhost:8080"),
		GraphClientState: envOr("GRAPH_CLIENT_STATE", "overseer-dev-client-state"),
		SubscriptionTTL:  48 * time.Hour, // Graph caps message subscriptions ~3 days
		RenewAhead:       12 * time.Hour,
		RenewInterval:    time.Hour,
		CheckupInterval:  time.Minute, // spec: scheduler ticks every 60s
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// logLevel maps LOG_LEVEL (debug|info|warn|error) to a slog.Level; anything
// unset or unrecognized means info. debug makes the LLM gateway emit raw
// model responses.
func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(envOr("LOG_LEVEL", "info"))); err != nil {
		return slog.LevelInfo
	}
	return l
}

// staticResolver implements MVP single-user auth: any bearer token maps to
// the seeded user; the token itself is passed through to Graph.
type staticResolver struct {
	user entity.User
}

func (r staticResolver) Resolve(_ context.Context, _ string) (entity.User, error) {
	return r.user, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel()}))
	slog.SetDefault(log)
	cfg := loadConfig()
	ctx := context.Background()

	// Schema first: dbmate applies pending migrations before anything runs.
	if err := repo.Migrate(cfg.DatabaseURL, overseer.MigrationsFS); err != nil {
		log.Error("migrate", "error", err)
		os.Exit(1)
	}

	pool, err := repo.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// MVP single-user mode: seed one account + one user row.
	users := repo.NewUsers(pool)
	seedUser, err := users.EnsureSeedUser(ctx, cfg.SeedAccountName, cfg.SeedUserEmail)
	if err != nil {
		log.Error("seed user", "error", err)
		os.Exit(1)
	}
	log.Info("seed user ready", "email", seedUser.Email, "userId", seedUser.ID)

	// Adapters.
	graphClient := graph.New()
	driveClient := drive.New()
	excelClient := excel.New()
	llmGateway := llm.New(cfg.LLMModel, log)
	orderRepo := repo.NewOrders(pool)
	subsRepo := repo.NewSubscriptions(pool)
	discRepo := repo.NewDiscrepancies(pool)
	ottoRepo := repo.NewOttoScans(pool)
	checkupRepo := repo.NewCheckups(pool)
	tokens := tokencache.New()
	hub := ws.NewHub(log)

	// Usecases.
	listOrders := &orders.ListOrders{Repo: orderRepo}
	buildList := &orders.BuildActiveOrderList{
		Graph:  graphClient,
		Format: &orders.FormatInitialOrderData{LLM: llmGateway},
		Repo:   orderRepo,
		Events: hub,
	}
	handleChange := &messages.HandleChangeMessage{
		Subs:   subsRepo,
		Tokens: tokens,
		Latest: &messages.GetLatestMessage{Graph: graphClient},
		LLM:    llmGateway,
		Repo:   orderRepo,
		Events: hub,
		Audit: &orders.CatchDiscrepancies{
			Graph:  graphClient,
			LLM:    llmGateway,
			Repo:   discRepo,
			Events: hub,
		},
		Checkups: checkupRepo,
	}
	ensureSub := &subscriptions.EnsureSubscription{
		Graph:           graphClient,
		Repo:            subsRepo,
		NotificationURL: cfg.PublicBaseURL + "/webhooks/graph",
		ClientState:     cfg.GraphClientState,
		TTL:             cfg.SubscriptionTTL,
		Now:             time.Now,
	}
	renewSubs := &subscriptions.RenewDueSubscriptions{
		Graph:      graphClient,
		Repo:       subsRepo,
		Tokens:     tokens,
		TTL:        cfg.SubscriptionTTL,
		RenewAhead: cfg.RenewAhead,
		Now:        time.Now,
		Log:        log,
	}
	sendDueCheckups := &checkups.SendDueCheckups{
		Repo:   checkupRepo,
		Mail:   graphClient,
		Tokens: tokens,
		Now:    time.Now,
		Log:    log,
	}

	// Controllers + router.
	ordersController := &httpctl.OrdersController{
		List:  listOrders,
		Build: buildList,
		SyncLookback: func() time.Time {
			return time.Now().AddDate(0, 0, -cfg.SyncLookbackDays)
		},
		Dispatch: func(fn func()) { go fn() },
		Log:      log,
	}

	e := httpctl.NewRouter(httpctl.RouterDeps{
		Resolver:      staticResolver{user: seedUser},
		Tokens:        tokens,
		Orders:        ordersController,
		Subscriptions: &httpctl.SubscriptionsController{Ensure: ensureSub, Log: log},
		Discrepancies: &httpctl.DiscrepanciesController{
			List:   &orders.ListDiscrepancies{Repo: discRepo},
			Update: &orders.UpdateDiscrepancyStatus{Repo: discRepo},
			Log:    log,
		},
		Otto: &httpctl.OttoController{
			Resolve: &otto.ResolveDestinations{Graph: graphClient, Drive: driveClient, LLM: llmGateway, Repo: ottoRepo},
			Record:  &otto.RecordOrder{Repo: ottoRepo, Excel: excelClient, LLM: llmGateway, Drive: driveClient},
			Log:     log,
		},
		Checkups: &httpctl.CheckupsController{
			Create: &checkups.CreateCheckupMessages{Repo: checkupRepo},
			List:   &checkups.ListCheckups{Repo: checkupRepo},
			Update: &checkups.UpdateStatus{Repo: checkupRepo},
			Log:    log,
		},
		Webhooks: &httpctl.WebhooksController{
			Handle:      handleChange,
			ClientState: cfg.GraphClientState,
			Dispatch:    func(fn func()) { go fn() },
			Log:         log,
		},
		WS:  hub,
		Log: log,
	})

	// Renewal worker: message subscriptions expire (~3 days max); renew
	// anything entering the RenewAhead window every RenewInterval.
	go func() {
		ticker := time.NewTicker(cfg.RenewInterval)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := renewSubs.Execute(ctx); err != nil {
				log.Error("subscription renewal pass failed", "error", err)
			} else if n > 0 {
				log.Info("subscriptions renewed", "count", n)
			}
		}
	}()

	// Checkup scheduler: every minute, send due follow-ups. One bad tick must
	// not kill the loop; ctx cancellation stops it cleanly.
	go func() {
		ticker := time.NewTicker(cfg.CheckupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := sendDueCheckups.Execute(ctx); err != nil {
					log.Error("checkup send pass failed", "error", err)
				} else if n > 0 {
					log.Info("checkups sent", "count", n)
				}
			}
		}
	}()

	log.Info("overseer listening", "port", cfg.Port)
	if err := e.Start(":" + cfg.Port); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
