package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"pam-chatcenter-mcp-proxy/internal/proxy"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	log.SetOutput(os.Stderr)

	cfg, err := proxy.ConfigFromEnv()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	handler, err := proxy.New(cfg)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch cfg.Transport {
	case "stdio":
		if err := handler.RunStdio(ctx, os.Stdin, os.Stdout); err != nil {
			log.Fatalf("stdio bridge: %v", err)
		}
	case "both":
		go func() {
			if err := handler.RunStdio(ctx, os.Stdin, os.Stdout); err != nil {
				log.Printf("stdio bridge: %v", err)
			}
		}()
		if err := handler.RunHTTP(ctx); err != nil {
			log.Fatalf("http server: %v", err)
		}
	default:
		if err := handler.RunHTTP(ctx); err != nil {
			log.Fatalf("http server: %v", err)
		}
	}
}
