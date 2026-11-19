package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	botTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	agentPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	keyPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{32,}$`)
)

func agentKeys(config string) (map[[sha256.Size]byte]string, error) {
	var agents map[string]string
	if err := json.Unmarshal([]byte(config), &agents); err != nil || len(agents) == 0 {
		return nil, errors.New("AGENT_KEYS must be a JSON object with at least one agent and key")
	}
	keys := make(map[[sha256.Size]byte]string, len(agents))
	for name, key := range agents {
		if !agentPattern.MatchString(name) || !keyPattern.MatchString(key) {
			return nil, errors.New("invalid agent name or key in AGENT_KEYS")
		}
		hash := sha256.Sum256([]byte(key))
		if _, exists := keys[hash]; exists {
			return nil, errors.New("agent keys must be unique")
		}
		keys[hash] = name
	}
	return keys, nil
}

func run(ctx context.Context) error {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if !botTokenPattern.MatchString(token) {
		return errors.New("set TELEGRAM_BOT_TOKEN to a valid bot token")
	}
	chatID, err := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
	if err != nil || chatID <= 0 {
		return errors.New("set TELEGRAM_CHAT_ID to your private Telegram chat ID")
	}
	keys, err := agentKeys(os.Getenv("AGENT_KEYS"))
	if err != nil {
		return err
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	// Message IDs are unique only within a chat.
	botID, _, _ := strings.Cut(token, ":")
	dataDir = filepath.Join(dataDir, botID, strconv.FormatInt(chatID, 10))
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	g := &gateway{
		bot: &telegram{
			client: &http.Client{
				Timeout: 35 * time.Second,
				CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
					return http.ErrUseLastResponse
				},
			},
			url:    "https://api.telegram.org/bot" + token,
			chatID: chatID,
		},
		store: store{dir: dataDir},
		keys:  keys,
	}
	server := &http.Server{
		Handler:           g.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	pollCtx, cancelPoll := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		g.poll(pollCtx)
	}()
	defer func() {
		cancelPoll()
		<-pollDone
	}()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()
	log.Printf("Agent API listening on %s", listener.Addr())
	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
