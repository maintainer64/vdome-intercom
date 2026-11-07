package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"vdome/internal/app"
	"vdome/internal/domain"
	"vdome/internal/provider"
	"vdome/internal/vault"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func main() {
	var p domain.Provider = provider.Unconfigured{}
	mode := env("VDOME_PROVIDER", "live")
	switch mode {
	case "demo":
		p = provider.NewDemo()
	case "live":
		live := provider.NewLive()
		if err := live.ConfigureSIP(os.Getenv("VDOME_SIP_WS"), os.Getenv("VDOME_SIP_REGISTRAR")); err != nil {
			log.Fatal(err)
		}
		p = live
	case "unconfigured":
	default:
		log.Fatal("VDOME_PROVIDER: допустимы live, demo и unconfigured")
	}
	var store app.Storage
	if key := os.Getenv("VDOME_VAULT_KEY"); key != "" {
		v, err := vault.New(env("VDOME_VAULT_FILE", "data/"+mode+".vault"), key)
		if err != nil {
			log.Fatal(err)
		}
		store = v
	} else if mode == "live" && os.Getenv("VDOME_MEMORY_ONLY") != "1" {
		v, err := vault.Local(env("VDOME_VAULT_FILE", "data/live.vault"), "data/vault.key")
		if err != nil {
			log.Fatal(err)
		}
		store = v
		log.Print("Сессии сохраняются в локальном зашифрованном vault; ключ: data/vault.key (0600)")
	} else {
		log.Print("Сессии хранятся только в памяти; для сохранения включите VDOME_VAULT_KEY")
	}
	m, err := app.New(p, store)
	if err != nil {
		log.Fatal(err)
	}
	defer m.Close()
	h, err := app.Handler(m, env("VDOME_ORIGIN", "http://127.0.0.1:8080"), env("VDOME_WEB_DIR", "web/dist"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			if err := m.Poll(ctx); err != nil && ctx.Err() == nil {
				log.Print("Синхронизация не завершена; автоматические команды при ошибке не повторяются")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	server := &http.Server{Addr: env("VDOME_ADDR", "127.0.0.1:8080"), Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("VDome: %s; provider=%s", server.Addr, mode)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
