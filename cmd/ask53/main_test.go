package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ask53/internal/config"
	"github.com/miekg/dns"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("ASK53_TEST_HELPER") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("ask53", flag.ExitOnError)
	os.Args = []string{"ask53", "-config", os.Getenv("ASK53_TEST_CONFIG")}
	if os.Getenv("ASK53_TEST_CHECK") == "1" {
		os.Args = append(os.Args, "-check-config")
	}
	os.Exit(run())
}
func process(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestProcessHelper$")
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "ASK53_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "ASK53_TEST_HELPER=1", "ASK53_TEST_CONFIG="+path)
	return cmd
}
func startProcess(t *testing.T, path string) (*exec.Cmd, string) {
	t.Helper()
	cmd := process(t, path)
	pipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			var event struct{ Msg, DNS string }
			json.Unmarshal(scanner.Bytes(), &event)
			if event.Msg == "listening" {
				ready <- event.DNS
			}
		}
	}()
	select {
	case addr := <-ready:
		return cmd, addr
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("process startup timed out")
		return nil, ""
	}
}
func stopProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("unclean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("process did not terminate")
	}
}
func TestProcessRestartPreservesBudgetAndCheckIsOffline(t *testing.T) {
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"content":"Tehran"},"finish_reason":"stop"}]}`)
	}))
	defer provider.Close()
	dir := t.TempDir()
	c := config.Defaults()
	c.Listen = "127.0.0.1:0"
	c.AdminListen = ""
	c.AIEndpoint = provider.URL + "/v1/chat/completions"
	c.Model = "fake"
	c.DailyCalls = 1
	c.BudgetFile = filepath.Join(dir, "budget.json")
	path := filepath.Join(dir, "config.json")
	b, _ := json.Marshal(c)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	check := process(t, path)
	check.Env = append(check.Env, "ASK53_TEST_CHECK=1")
	if out, err := check.CombinedOutput(); err != nil || !strings.Contains(string(out), "configuration valid") {
		t.Fatal("offline validation", string(out), err)
	}
	if _, err := os.Stat(c.BudgetFile); !os.IsNotExist(err) || calls.Load() != 0 {
		t.Fatal("validation touched budget/provider")
	}
	query := func(addr, name string) *dns.Msg {
		t.Helper()
		q := new(dns.Msg)
		q.SetQuestion(name, dns.TypeTXT)
		r, _, err := (&dns.Client{Net: "tcp", Timeout: time.Second}).ExchangeContext(context.Background(), q, addr)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	cmd, addr := startProcess(t, path)
	if r := query(addr, "capital.of.iran."); r.Rcode != 0 {
		t.Fatal(r)
	}
	stopProcess(t, cmd)
	cmd, addr = startProcess(t, path)
	if r := query(addr, "capital.of.france."); r.Rcode != dns.RcodeServerFailure {
		t.Fatal("restart bypassed budget", r)
	}
	stopProcess(t, cmd)
	if calls.Load() != 1 {
		t.Fatal("restart caused extra provider calls", calls.Load())
	}
}
