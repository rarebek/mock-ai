package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	Environment              string
	HTTPAddr                 string
	DatabaseURL              string
	WebOrigin                string
	AdminOrigin              string
	AIKeyEncryptionKey       string
	GeminiWritingModel       string
	GeminiContentModel       string
	GeminiSpeakingModel      string
	GeminiSpeakingVoiceModel string
	GeminiSpeakingLiveModel  string
	TelegramBotToken         string
	TelegramMiniAppURL       string
	GoogleClientID           string
	GoogleClientSecret       string
	GoogleRedirectURL        string
	SessionSecret            string
	SessionTTL               time.Duration
	SecureCookie             bool
	// PaymentProvider selects how stars are sold ("" or "none" = coming soon).
	PaymentProvider string
}

func Load() (Config, error) {
	cfg := Config{
		Environment:              envOr("APP_ENV", "development"),
		HTTPAddr:                 envOr("HTTP_ADDR", ":8080"),
		DatabaseURL:              os.Getenv("DATABASE_URL"),
		AIKeyEncryptionKey:       os.Getenv("AI_KEY_ENCRYPTION_KEY"),
		GeminiWritingModel:       envOr("GEMINI_WRITING_MODEL", "gemini-3.7-flash"),
		GeminiContentModel:       envOr("GEMINI_CONTENT_MODEL", "gemini-3.7-flash"),
		GeminiSpeakingModel:      envOr("GEMINI_SPEAKING_MODEL", "gemini-3.7-flash"),
		GeminiSpeakingVoiceModel: envOr("GEMINI_SPEAKING_VOICE_MODEL", "gemini-3.8-flash-tts"),
		GeminiSpeakingLiveModel:  envOr("GEMINI_SPEAKING_LIVE_MODEL", "gemini-3.8-live"),
		WebOrigin:                envOr("WEB_ORIGIN", "http://localhost:5173"),
		PaymentProvider:          envOr("PAYMENT_PROVIDER", "none"),
		TelegramBotToken:         os.Getenv("TELEGRAM_BOT_TOKEN"),
		GoogleClientID:           os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:       os.Getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRedirectURL:        envOr("GOOGLE_REDIRECT_URL", "http://localhost:8080/api/v1/auth/google/callback"),
		SessionSecret:            os.Getenv("SESSION_SECRET"),
		SessionTTL:               30 * 24 * time.Hour,
	}
	cfg.TelegramMiniAppURL = envOr("TELEGRAM_MINI_APP_URL", cfg.WebOrigin)
	if cfg.AIKeyEncryptionKey != "" {
		key, e := hex.DecodeString(cfg.AIKeyEncryptionKey)
		if e != nil || len(key) != 32 {
			return Config{}, fmt.Errorf("AI_KEY_ENCRYPTION_KEY must contain 32 hex-encoded bytes")
		}
	}
	cfg.SecureCookie = cfg.Environment != "development"
	if cfg.SecureCookie {
		cfg.AdminOrigin = envOr("ADMIN_ORIGIN", "https://mockai.admin.chessinsight.uz")
	} else {
		cfg.AdminOrigin = envOr("ADMIN_ORIGIN", cfg.WebOrigin)
	}
	if cfg.HTTPAddr == "" {
		return Config{}, fmt.Errorf("HTTP_ADDR must not be empty")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must not be empty")
	}
	if len(cfg.SessionSecret) < 32 || strings.HasPrefix(cfg.SessionSecret, "replace-") {
		return Config{}, fmt.Errorf("SESSION_SECRET must contain at least 32 random characters")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
