package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	adminapp "github.com/own/mock-ai/apps/api/internal/admin/application"
	adminhttp "github.com/own/mock-ai/apps/api/internal/admin/httpapi"
	adminpg "github.com/own/mock-ai/apps/api/internal/admin/postgres"

	examapp "github.com/own/mock-ai/apps/api/internal/exam/application"
	geminiimport "github.com/own/mock-ai/apps/api/internal/exam/geminiimport"
	examhttp "github.com/own/mock-ai/apps/api/internal/exam/httpapi"
	exampostgres "github.com/own/mock-ai/apps/api/internal/exam/postgres"
	"github.com/own/mock-ai/apps/api/internal/identity"
	"github.com/own/mock-ai/apps/api/internal/identity/googleauth"
	identityhttp "github.com/own/mock-ai/apps/api/internal/identity/httpapi"
	identitypostgres "github.com/own/mock-ai/apps/api/internal/identity/postgres"
	"github.com/own/mock-ai/apps/api/internal/platform/config"
	"github.com/own/mock-ai/apps/api/internal/platform/httpserver"
	db "github.com/own/mock-ai/apps/api/internal/platform/postgres/sqlc"
	speakingapp "github.com/own/mock-ai/apps/api/internal/speaking/application"
	speakinggemini "github.com/own/mock-ai/apps/api/internal/speaking/gemini"
	speakinghttp "github.com/own/mock-ai/apps/api/internal/speaking/httpapi"
	speakinglive "github.com/own/mock-ai/apps/api/internal/speaking/live"
	speakingpg "github.com/own/mock-ai/apps/api/internal/speaking/postgres"
	starsapp "github.com/own/mock-ai/apps/api/internal/stars/application"
	starshttp "github.com/own/mock-ai/apps/api/internal/stars/httpapi"
	"github.com/own/mock-ai/apps/api/internal/stars/payment"
	starspg "github.com/own/mock-ai/apps/api/internal/stars/postgres"
	"github.com/own/mock-ai/apps/api/internal/telegrambot"
	writingapp "github.com/own/mock-ai/apps/api/internal/writing/application"
	"github.com/own/mock-ai/apps/api/internal/writing/gemini"
	writinghttp "github.com/own/mock-ai/apps/api/internal/writing/httpapi"
	writingpg "github.com/own/mock-ai/apps/api/internal/writing/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		logger.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	if err := pool.Ping(connectCtx); err != nil {
		cancelConnect()
		logger.Error("connect to database", "error", err)
		os.Exit(1)
	}
	cancelConnect()

	identityRepository := identitypostgres.New(pool)
	identityService, err := identity.NewService(identityRepository, cfg.TelegramBotToken, cfg.SessionTTL)
	if err != nil {
		logger.Error("initialize identity service", "error", err)
		os.Exit(1)
	}
	googleProvider := googleauth.New(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL, cfg.SessionSecret)
	identityHandler := identityhttp.New(identityService, googleProvider, logger, cfg.WebOrigin, cfg.SecureCookie)
	examRepository := exampostgres.New(pool)
	examService := examapp.NewService(examRepository)
	if cfg.AIKeyEncryptionKey != "" {
		examService.ConfigureImporter(geminiimport.New(db.New(pool), cfg.AIKeyEncryptionKey), cfg.GeminiContentModel, logger)
	}
	adminHandler := adminhttp.New(adminapp.New(adminpg.New(pool, cfg.AIKeyEncryptionKey)), cfg.AdminOrigin, cfg.SecureCookie)
	examHandler := examhttp.New(examService, identityHandler, logger, adminHandler)
	var evaluator writingapp.Evaluator
	if cfg.AIKeyEncryptionKey != "" {
		evaluator = gemini.New(db.New(pool), cfg.AIKeyEncryptionKey)
	}
	writingService := writingapp.New(writingpg.New(pool), evaluator, cfg.GeminiWritingModel)
	writingHandler := writinghttp.New(writingService, identityHandler, cfg.WebOrigin)
	var speakingProvider speakingapp.Provider
	if cfg.AIKeyEncryptionKey != "" {
		speakingProvider = speakinggemini.New(db.New(pool), cfg.AIKeyEncryptionKey, cfg.GeminiSpeakingVoiceModel)
	}
	speakingService := &speakingapp.Service{Repo: speakingpg.New(pool, cfg.AIKeyEncryptionKey), Provider: speakingProvider, Model: cfg.GeminiSpeakingModel, Log: logger}
	speakingHandler := speakinghttp.New(speakingService, identityHandler, cfg.WebOrigin)
	if provider, ok := speakingProvider.(speakinglive.Provider); ok {
		speakingHandler.Live = &speakinglive.Service{Repo: speakingService.Repo.(*speakingpg.Repository), Provider: provider, Model: cfg.GeminiSpeakingLiveModel, Log: logger}
	}
	paymentProvider, err := payment.New(payment.Config{
		Provider: cfg.PaymentProvider, PublicBaseURL: cfg.WebOrigin,
		InpaySecretKey: cfg.InpaySecretKey, InpayWebhookSecret: cfg.InpayWebhookSecret, InpayBaseURL: cfg.InpayBaseURL,
		Log: logger,
	})
	if err != nil {
		logger.Error("initialize payment provider", "error", err)
		os.Exit(1)
	}
	starsService := starsapp.New(starspg.New(pool), paymentProvider, 1, cfg.WebOrigin, cfg.AdminOrigin)
	starsHandler := starshttp.New(starsService, identityHandler, adminHandler, cfg.WebOrigin)
	// Practice Writing evaluations and Speaking interviews are paid for with
	// practice credits (or a paid full mock's allowance).
	writingService.Credits = starsService
	speakingService.Credits = starsService
	bot, err := telegrambot.New(cfg.TelegramBotToken, cfg.TelegramMiniAppURL, cfg.WebOrigin, identityService, logger)
	if err != nil {
		logger.Error("initialize Telegram bot", "error", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpserver.New(logger, cfg.WebOrigin, identityHandler, examHandler, adminHandler, writingHandler, speakingHandler, starsHandler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go writingService.Run(ctx)
	go starsService.Run(ctx)
	examService.RunImports(ctx)
	speakingService.Run(ctx)
	if bot != nil {
		go bot.Run(ctx)
		go bot.RunReminders(ctx, pool)
	} else {
		logger.Info("Telegram bot disabled")
	}

	go func() {
		logger.Info("api listening", "address", cfg.HTTPAddr, "environment", cfg.Environment)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("api stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
}
