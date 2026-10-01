package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/MeghdadFadaee/ask-53/internal/ai"
	"github.com/MeghdadFadaee/ask-53/internal/config"
	"github.com/MeghdadFadaee/ask-53/internal/dnsserver"
	"github.com/MeghdadFadaee/ask-53/internal/service"
)

var version = "dev"

func main() { os.Exit(run()) }
func run() int {
	configPath := flag.String("config", "", "JSON configuration file (optional)")
	check := flag.Bool("check-config", false, "validate configuration and exit without network calls")
	showVersion := flag.Bool("version", false, "print build version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	c, err := config.Load(*configPath)
	if err != nil {
		log.Error("configuration invalid", "error", err)
		return 1
	}
	if *check {
		fmt.Println("configuration valid")
		return 0
	}
	b, err := service.OpenBudget(c.BudgetFile, c.DailyCalls)
	if err != nil {
		log.Error("budget initialization failed", "error", err)
		return 1
	}
	defer b.Close()
	client := ai.New(c)
	defer client.Close()
	g := service.NewGate(c)
	stats := new(service.Stats)
	engine := service.NewEngine(c, client, b, g, stats, log)
	defer engine.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server, err := dnsserver.Start(c, engine, g, stats)
	if err != nil {
		log.Error("listen failed", "error", err)
		return 1
	}
	log.Info("listening", "dns", server.Addr(), "admin", server.AdminAddr(), "udp_mode", c.UDPMode, "daily_calls", c.DailyCalls)
	exitCode := 0
	select {
	case <-ctx.Done():
	case err = <-server.Errors():
		log.Error("listener stopped", "error", err)
		exitCode = 1
	}
	shutdown, cancel := context.WithTimeout(context.Background(), c.ShutdownTimeout.Value())
	defer cancel()
	if err = server.Close(shutdown); err != nil {
		log.Error("drain failed", "error", err)
		exitCode = 1
	}
	engine.Close()
	log.Info("shutdown complete", "counters", stats.Snapshot())
	return exitCode
}
