// mock-provider is a deterministic local test tool. It never calls an AI API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8081", "loopback HTTP address")
	delay := flag.Duration("delay", 100*time.Millisecond, "simulated provider delay")
	flag.Parse()
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("mock provider must listen on a loopback IP")
	}
	var calls atomic.Uint64
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		var body struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 2 {
			http.Error(w, "bad request", 400)
			return
		}
		calls.Add(1)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(*delay):
		}
		answer := "Unknown"
		switch body.Messages[1].Content {
		case "capital of iran":
			answer = "Tehran"
		case "capital of france":
			answer = "Paris"
		case "provider fail":
			http.Error(w, "simulated provider failure", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": answer}, "finish_reason": "stop"}}})
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]uint64{"calls": calls.Load()})
	})
	s := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		drain, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.Shutdown(drain)
	}()
	log.Printf("local fake provider listening on %s", *listen)
	if err = s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
