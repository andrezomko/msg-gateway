# Bot setup and service review

Review date: 18 November 2026.

## Is the service ready?

**The service is ready for a local trial. Production use needs more work.**

The code sends text, identifies the agent, saves message ownership, and saves
your replies. Each agent can read only the replies to its own messages.
The automated test uses a local Telegram test server. A test with a real bot
was not done. No bot credentials or deployment settings were supplied.

This service is a message gateway. It does not create AI agents, run an AI
model, or act on a reply. Each agent must call the HTTP API and process replies.

## 1. Select the bot arrangement

- To connect several agents to your private Telegram chat, use one Telegram
  bot, one gateway process, and one API key for each agent.
- To use separate Telegram bots, start one gateway process for each bot.
  Use a different `LISTEN_ADDR` port for each process.
- Each process supports one private chat. Group chats and channels are not
  supported. Only text replies to messages sent by this gateway are saved.

Only one process must receive updates for a bot. Do not run another polling
client for the same bot while the gateway runs.

## 2. Create the Telegram bot

1. Open [@BotFather](https://t.me/BotFather) in Telegram.
2. Send `/newbot`. Select a name and username. Keep the token that BotFather gives you.
3. Open your new bot in a private chat. Send `/start`.
4. Keep the gateway stopped during the next steps.

See the [Telegram bot creation instructions](https://core.telegram.org/bots/features#creating-a-new-bot).
No group permissions, inline mode, or bot command menu is needed for this service.

## 3. Check the token and get your chat ID

Run these commands in Bash. You need `curl` and `openssl`.

```bash
read -rsp 'Telegram bot token: ' TELEGRAM_BOT_TOKEN
printf '\n'
export TELEGRAM_BOT_TOKEN
curl -fsS "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getMe"
curl -fsS "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getWebhookInfo"
```

Check that `getMe` returns `"ok":true` and the correct bot. If the webhook
`url` is not empty, stop the old bot service and remove its webhook:

```bash
curl -fsS -X POST \
  "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/deleteWebhook" \
  --data 'drop_pending_updates=false'
```

The gateway uses polling. Telegram does not permit polling while a webhook
is set. See [deleteWebhook](https://core.telegram.org/bots/api#deletewebhook).

Send a new text message to the bot. Then run:

```bash
curl -fsS -X POST \
  "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/getUpdates" \
  --data 'allowed_updates=["message"]'
read -rp 'Private chat ID: ' TELEGRAM_CHAT_ID
export TELEGRAM_CHAT_ID
```

Enter the positive `message.chat.id` from your private chat update. Do not
use the bot ID or a group ID. If the result is empty, send a new message and
try again. After startup, let the gateway make all `getUpdates` calls.

## 4. Set the agent keys and start

Run from the repository directory. Install Go 1.25 or later.

```bash
BUILD_KEY=$(openssl rand -hex 32)
REVIEW_KEY=$(openssl rand -hex 32)
export AGENT_KEYS="{\"build\":\"${BUILD_KEY}\",\"review\":\"${REVIEW_KEY}\"}"
export LISTEN_ADDR=127.0.0.1:8080
export DATA_DIR="$PWD/data"
go build -o msg-gateway .
./msg-gateway
```

The process stays in the foreground. Keep it running. Use another terminal
for API requests, with the applicable agent key set in that terminal.

- `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, and `AGENT_KEYS` are required.
- Agent names permit letters, digits, `_`, and `-`. The first character must
  be a letter or digit. Maximum length: 64 characters.
- Keys must be unique. They need at least 32 characters from letters,
  digits, `_`, and `-`. The commands above create suitable random keys.
- Give each agent its gateway URL and its own key. Keep the Telegram token
  and other agents' keys in the gateway environment.
- Save the environment settings in your service manager or a protected
  configuration file before a restart. The gateway does not load `.env` files.
  Do not generate new keys on each start unless you also update all clients.
- Keep the same agent names and data directory. You can change a key while
  keeping its agent name. Restart the gateway to load configuration changes.

The default address is `127.0.0.1:8080`. The default data directory is `./data`.
Actual storage is under `DATA_DIR/<bot-id>/<chat-id>/`. Use an absolute path
for a managed service. The process user needs write access.

## 5. Connect and test each agent

In a second terminal, set `BUILD_KEY` to the saved build agent key. Then send:

```bash
curl -fsS http://127.0.0.1:8080/v1/messages \
  -H "Authorization: Bearer ${BUILD_KEY}" \
  -H 'Content-Type: application/json' \
  --data '{"text":"Can I continue?"}'
```

Expect HTTP 201 and `{"id":42}`, with your actual message ID.
In Telegram, select **Reply** on that message and send text. A separate
message without Reply is ignored. Replace `42` below with the returned ID:

```bash
curl -fsS http://127.0.0.1:8080/v1/messages/42/replies \
  -H "Authorization: Bearer ${BUILD_KEY}"
```

Expect `{"replies":[{"id":43,"text":"Yes.","date":"..."}]}`.
Before your reply arrives, expect `{"replies":[]}`.

Each agent must save its sent message IDs, poll for replies, and save the
reply IDs it has processed. The API returns all stored replies on each read.
As an initial client setting, poll every 2–5 seconds while a reply is needed.
This interval is a client choice; the service does not enforce it.

Repeat with the review agent key. Confirm that the review key gets HTTP 404
when it reads the build agent's message. Restart the gateway and confirm that
the build key can still read the saved reply.

## 6. Required work before production

These findings come from code inspection. The failure cases below do not
have automated tests in this repository.

1. **Add a readiness check.** `/healthz` always returns 204 when its handler
   runs (`gateway.go:31`). Startup checks the token format, but does not call
   `getMe` or check the webhook (`main.go:46`). Invalid credentials or a
   polling conflict can leave the HTTP API running without incoming replies.
   Check token access, webhook state, storage writes, and polling progress.
   Keep `/healthz` as a process check.
2. **Add recovery for storage errors.** Sending to Telegram happens before
   ownership is saved (`gateway.go:79`, `gateway.go:92`). A crash or write
   failure in between can leave a delivered message without an owner. Replies
   to it are ignored (`gateway.go:178`). Define a recovery procedure or add
   durable send tracking. A corrupt owner or reply file can also stop
   processing the rest of an update batch (`gateway.go:143`, `store.go:47`).
   Add an alert and a safe repair or quarantine procedure.
3. **Define send retry rules.** There is no request ID for duplicate prevention
   and no durable send queue. A timeout or HTTP 502 can occur after delivery.
   Check the chat before a retry. HTTP 500 with an `id` means Telegram accepted
   the message but ownership was not saved. Do not automatically resend it.
   On HTTP 429, wait for the `Retry-After` interval before a retry. Add request
   tracking if clients need automatic retries with duplicate control.
4. **Add failure tests.** Cover bad configuration, Telegram 401/409/429,
   timeouts, storage errors, replay after a polling restart, and concurrent
   sends and replies. The existing single test covers the normal send/reply
   flow, missing authorization, empty text, chat filtering, duplicate reply
   removal, stored reply reads, and access separation between agents.
5. **Prepare the runtime.** Set up automatic restart, protected environment
   settings, a persistent data directory, backup and restore, log monitoring,
   and a data retention rule. For agents on other hosts, use a TLS reverse
   proxy and restrict access to the API. The service has no built-in TLS or
   per-agent request rate limit. It needs outbound HTTPS to `api.telegram.org`;
   it does not need an inbound Telegram webhook.

The repository has no service unit, container setup, proxy setup, or agent
client. Provide these for your selected runtime. A container is optional.
The file store has no automatic cleanup or paging of reply results.

Telegram keeps pending updates for at most 24 hours. A longer outage can
lose replies that the gateway has not received. See
[Telegram update delivery](https://core.telegram.org/bots/api#getting-updates).

## Review checks

The following checks passed on the reviewed working files with Go 1.25.3:

- `go test -race -cover ./...`: passed; statement coverage was 55.3%.
- `go vet ./...`: passed.
- `go build -o /tmp/msg-gateway-review .`: passed.

Production acceptance still needs the real-bot test in step 5, a restore
test, and resolution of the relevant failure cases in step 6. For a local
trial, complete steps 1–5 and keep the documented limits in use.
