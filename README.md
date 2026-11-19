# Telegram gateway for agents

This Go service lets agents send text to one private Telegram chat and read your replies through an HTTP API. Each agent has a separate API key and can read replies to its own messages. The gateway stores message ownership and replies on disk.

## Start the service

1. Install Go 1.25 or later.
2. Create a bot with [@BotFather](https://t.me/BotFather). Open a private chat with the bot and send `/start`.
3. Find your private chat ID. With the gateway stopped, run these commands. Find `message.chat.id` in the update for your private chat. [Telegram Bot API documentation](https://core.telegram.org/bots/api#getupdates)

   ```bash
   read -rsp 'Bot token: ' TELEGRAM_BOT_TOKEN
   printf '\n'
   export TELEGRAM_BOT_TOKEN
   curl -fsS "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates"
   ```

4. Set the chat ID, generate a key for each agent, and start the service. Give each agent only its own key.

   ```bash
   export TELEGRAM_CHAT_ID=123456789
   BUILD_KEY=$(openssl rand -hex 32)
   REVIEW_KEY=$(openssl rand -hex 32)
   export AGENT_KEYS="{\"build\":\"${BUILD_KEY}\",\"review\":\"${REVIEW_KEY}\"}"
   go run .
   ```

`LISTEN_ADDR` defaults to `127.0.0.1:8080`. For agents on other hosts, put the API behind a TLS reverse proxy. `DATA_DIR` defaults to `./data`. Keep this directory and the same agent names between restarts. You can change an agent's key without losing access to its replies.

Run one gateway instance per bot. The gateway uses Telegram long polling. If your bot already has a webhook, remove it before starting the gateway. [Telegram webhook documentation](https://core.telegram.org/bots/api#deletewebhook)

## Send a message and read replies

Send a message with the agent key. The gateway adds the agent name to the message in Telegram.

```bash
curl -fsS http://127.0.0.1:8080/v1/messages \
  -H "Authorization: Bearer ${BUILD_KEY}" \
  -H 'Content-Type: application/json' \
  --data '{"text":"Can I continue?"}'
```

A successful request returns HTTP 201 and a message ID, for example `{"id":42}`. The Telegram limit is 4,096 characters, including the agent name added by the gateway. [Telegram sendMessage documentation](https://core.telegram.org/bots/api#sendmessage)

In Telegram, use **Reply** on the agent's message and send your text. Use the ID from the send request to read the replies.

```bash
curl -fsS http://127.0.0.1:8080/v1/messages/42/replies \
  -H "Authorization: Bearer ${BUILD_KEY}"
```

The API returns an array of replies. Each reply has an `id`, `text`, and UTC `date`. An empty array means there is no reply yet. Agents can poll this endpoint and use reply IDs to process each reply once.

`GET /healthz` returns HTTP 204 when the HTTP server is running. Telegram can return HTTP 429 with a `Retry-After` header. If a send returns HTTP 502 or the connection closes before the response, check the Telegram chat before you retry. Telegram may already have accepted the message.

## Test

```bash
go test -race ./...
```
