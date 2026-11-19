package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type gateway struct {
	bot   *telegram
	store store
	keys  map[[sha256.Size]byte]string

	// Wait for active sends to record ownership before routing incoming replies.
	sends sync.RWMutex
}

func (g *gateway) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/messages", g.authenticate(g.send))
	mux.HandleFunc("GET /v1/messages/{id}/replies", g.authenticate(g.getReplies))
	return mux
}

func (g *gateway) authenticate(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		agent, ok := g.keys[sha256.Sum256([]byte(token))]
		if !strings.EqualFold(scheme, "Bearer") || !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeResponse(w, http.StatusUnauthorized, map[string]string{"error": "valid agent key required"})
			return
		}
		next(w, r, agent)
	}
}

func (g *gateway) send(w http.ResponseWriter, r *http.Request, agent string) {
	var message struct {
		Text string `json:"text"`
	}
	body := http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeResponse(w, http.StatusBadRequest, map[string]string{"error": "expected a JSON object with a text field"})
		return
	}

	if strings.TrimSpace(message.Text) == "" {
		writeResponse(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}
	text := "[" + agent + "]\n" + message.Text
	if utf8.RuneCountInString(text) > 4096 {
		writeResponse(w, http.StatusBadRequest, map[string]string{"error": "message with agent name exceeds 4096 characters"})
		return
	}

	g.sends.RLock()
	defer g.sends.RUnlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, err := g.bot.send(ctx, text)
	if err != nil {
		var apiError *telegramError
		if errors.As(err, &apiError) && apiError.code == http.StatusTooManyRequests {
			seconds := max(1, int(apiError.retryAfter/time.Second))
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			writeResponse(w, http.StatusTooManyRequests, map[string]string{"error": "Telegram rate limit"})
			return
		}
		log.Printf("Send message for %s: %v", agent, err)
		writeResponse(w, http.StatusBadGateway, map[string]string{"error": "Telegram did not confirm delivery"})
		return
	}
	if err := g.store.setOwner(id, agent); err != nil {
		log.Printf("Record owner for Telegram message %d: %v", id, err)
		writeResponse(w, http.StatusInternalServerError, map[string]any{
			"error": "Telegram accepted the message, but the gateway could not save its owner",
			"id":    id,
		})
		return
	}
	writeResponse(w, http.StatusCreated, map[string]int64{"id": id})
}

func (g *gateway) getReplies(w http.ResponseWriter, r *http.Request, agent string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeResponse(w, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	}
	owner, err := g.store.owner(id)
	switch {
	case errors.Is(err, os.ErrNotExist), err == nil && owner != agent:
		writeResponse(w, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	case err != nil:
		log.Printf("Read owner for Telegram message %d: %v", id, err)
		writeResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not read message owner"})
		return
	}
	replies, err := g.store.replies(id)
	if err != nil {
		log.Printf("Read replies for Telegram message %d: %v", id, err)
		writeResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not read replies"})
		return
	}
	writeResponse(w, http.StatusOK, map[string][]reply{"replies": replies})
}

func writeResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (g *gateway) poll(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		updates, err := g.bot.updates(ctx, offset)
		if err == nil {
			if len(updates) == 0 {
				// This request confirmed previous updates. A future ID can reset after inactivity.
				offset = 0
			}
			for _, update := range updates {
				if err = g.recordReply(update.Message); err != nil {
					break
				}
				offset = max(offset, update.ID+1)
			}
		}
		if err == nil || ctx.Err() != nil {
			continue
		}
		log.Printf("Receive Telegram updates: %v", err)
		delay := 2 * time.Second
		var apiError *telegramError
		if errors.As(err, &apiError) {
			delay = max(delay, apiError.retryAfter)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (g *gateway) recordReply(message *telegramMessage) error {
	if message == nil || message.Chat.Type != "private" || message.Chat.ID != g.bot.chatID ||
		message.From == nil || message.From.ID != g.bot.chatID || message.From.IsBot ||
		message.ReplyTo == nil || message.ID <= 0 || strings.TrimSpace(message.Text) == "" {
		return nil
	}
	parentID := message.ReplyTo.ID

	g.sends.Lock()
	defer g.sends.Unlock()
	if _, err := g.store.owner(parentID); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("read owner for Telegram message %d: %w", parentID, err)
	}

	return g.store.addReply(parentID, reply{
		ID:   message.ID,
		Text: message.Text,
		Date: time.Unix(message.Date, 0).UTC(),
	})
}
