package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayRoutesTelegramReply(t *testing.T) {
	const buildKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const reviewKey = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	keys, err := agentKeys(fmt.Sprintf(`{"build":%q,"review":%q}`, buildKey, reviewKey))
	if err != nil {
		t.Fatal(err)
	}
	var polls atomic.Int32
	confirmed := make(chan struct{})
	bot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sendMessage":
			var body struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.ChatID != 123 || body.Text != "[build]\nCan I continue?" {
				t.Errorf("unexpected Telegram message: %+v", body)
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":42}}`)
		case "/getUpdates":
			var body struct {
				Offset int64 `json:"offset"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if polls.Add(1) == 1 {
				fmt.Fprint(w, `{"ok":true,"result":[
					{"update_id":8,"message":{"message_id":43,"date":100,"text":"wrong chat","chat":{"id":456,"type":"private"},"from":{"id":456},"reply_to_message":{"message_id":42}}},
					{"update_id":9,"message":{"message_id":44,"date":100,"text":"Yes.","chat":{"id":123,"type":"private"},"from":{"id":123},"reply_to_message":{"message_id":42}}},
					{"update_id":10,"message":{"message_id":44,"date":100,"text":"Yes.","chat":{"id":123,"type":"private"},"from":{"id":123},"reply_to_message":{"message_id":42}}}
				]}`)
				return
			}
			if body.Offset != 11 {
				t.Errorf("Telegram update offset = %d, want 11", body.Offset)
			}
			close(confirmed)
			<-r.Context().Done()
		default:
			t.Errorf("unexpected Telegram path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer bot.Close()

	botClient := &telegram{client: bot.Client(), url: bot.URL, chatID: 123}
	disk := store{dir: t.TempDir()}
	g := &gateway{bot: botClient, store: disk, keys: keys}
	request := func(handler http.Handler, method, path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, rec.Code, want, rec.Body)
		}
		return rec
	}

	handler := g.handler()
	request(handler, "POST", "/v1/messages", "", `{"text":"Can I continue?"}`, http.StatusUnauthorized)
	request(handler, "POST", "/v1/messages", buildKey, `{"text":""}`, http.StatusBadRequest)
	sent := request(handler, "POST", "/v1/messages", buildKey, `{"text":"Can I continue?"}`, http.StatusCreated)
	if strings.TrimSpace(sent.Body.String()) != `{"id":42}` {
		t.Fatalf("unexpected sent message: %s", sent.Body)
	}

	const repliesPath = "/v1/messages/42/replies"
	empty := request(handler, "GET", repliesPath, buildKey, "", http.StatusOK)
	if strings.TrimSpace(empty.Body.String()) != `{"replies":[]}` {
		t.Fatalf("unexpected empty replies: %s", empty.Body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		g.poll(ctx)
	}()
	defer func() {
		cancel()
		<-pollDone
	}()
	select {
	case <-confirmed:
	case <-ctx.Done():
		t.Fatal("gateway did not confirm Telegram updates")
	}

	restarted := (&gateway{bot: botClient, store: disk, keys: keys}).handler()
	request(restarted, "GET", repliesPath, reviewKey, "", http.StatusNotFound)
	got := request(restarted, "GET", repliesPath, buildKey, "", http.StatusOK)
	var result struct {
		Replies []reply `json:"replies"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Replies) != 1 || result.Replies[0].ID != 44 || result.Replies[0].Text != "Yes." {
		t.Fatalf("unexpected replies: %+v", result.Replies)
	}
}
