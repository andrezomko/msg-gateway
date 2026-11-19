package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type telegram struct {
	client *http.Client
	url    string
	chatID int64
}

type telegramMessage struct {
	ID   int64  `json:"message_id"`
	Date int64  `json:"date"`
	Text string `json:"text"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From *struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	} `json:"from"`
	ReplyTo *telegramMessage `json:"reply_to_message"`
}

type telegramUpdate struct {
	ID      int64            `json:"update_id"`
	Message *telegramMessage `json:"message"`
}

type telegramError struct {
	code       int
	retryAfter time.Duration
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("Telegram API error %d", e.code)
}

func (t *telegram) call(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := t.client.Do(req)
	if err != nil {
		// The full URL contains the bot token.
		return errors.New("Telegram request failed")
	}
	defer res.Body.Close()

	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	err = json.NewDecoder(res.Body).Decode(&envelope)
	if res.StatusCode != http.StatusOK || envelope.ErrorCode != 0 {
		code := envelope.ErrorCode
		if code == 0 {
			code = res.StatusCode
		}
		return &telegramError{code: code, retryAfter: time.Duration(envelope.Parameters.RetryAfter) * time.Second}
	}
	if err != nil || !envelope.OK {
		return errors.New("invalid Telegram response")
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return errors.New("invalid Telegram result")
	}
	return nil
}

func (t *telegram) send(ctx context.Context, text string) (int64, error) {
	var message telegramMessage
	err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id": t.chatID,
		"text":    text,
	}, &message)
	if err == nil && message.ID <= 0 {
		err = errors.New("Telegram did not return a message ID")
	}
	return message.ID, err
}

func (t *telegram) updates(ctx context.Context, offset int64) ([]telegramUpdate, error) {
	var updates []telegramUpdate
	err := t.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         25,
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}
