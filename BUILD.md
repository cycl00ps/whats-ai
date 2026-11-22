# Build, deploy, and use WhatsApp MCP

## Docker Compose quickstart

Install Docker with Compose. From the repository root, create the environment file:

```sh
umask 077
cp -n .env.example .env
openssl rand -hex 32
openssl rand -hex 32
```

Put one generated value in `MCP_BEARER_TOKEN` and the other in `WORKER_BEARER_TOKEN` in `.env`. Leave `PUBLIC_URL=http://localhost:8080` for a client on the same machine. Then run:

```sh
docker compose up --build -d
docker compose ps
docker compose logs --tail=100 whatsapp-mcp whatsapp-worker
```

The gateway publishes port `8080`. The worker listens on port `8081` inside the Compose network. Only the worker mounts the `whatsapp-data` volume, which contains SQLite at `/data/whatsapp-mcp.db`. Media staging uses the worker's temporary filesystem.

## Native build and deployment

Build with Go 1.26 or later:

```sh
go test ./...
CGO_ENABLED=0 go build -o whatsapp-mcp ./cmd/whatsapp-mcp
./whatsapp-mcp --version
```

Set the same `WORKER_BEARER_TOKEN` in both terminals and set `MCP_BEARER_TOKEN` in the gateway terminal. Start the worker first:

```sh
# Terminal 1
./whatsapp-mcp --worker

# Terminal 2
HTTP_ADDR=127.0.0.1:8080 ./whatsapp-mcp
```

Install FFmpeg on the worker host if you need to convert audio to WhatsApp voice messages. The Compose image includes it. Release archives contain the same binary for both roles.

### Linux systemd user services

Install the release RPM with `sudo dnf install ./whatsapp-mcp-*.rpm`. Put both tokens in `~/.config/whatsapp-mcp/env` and set the file mode to `0600`. Use one `NAME=value` assignment per line.

```sh
systemctl --user daemon-reload
systemctl --user enable --now whatsapp-mcp.service
systemctl --user status whatsapp-mcp.service whatsapp-worker.service
journalctl --user -u whatsapp-mcp.service -u whatsapp-worker.service -f
```

The gateway unit starts the worker unit. Use `loginctl enable-linger "$USER"` if the services should keep running after you log out. The worker's default database is under `~/.local/state/whatsapp-mcp/store/`.

### Configuration and upgrades

| Setting | Process | Purpose |
| --- | --- | --- |
| `MCP_BEARER_TOKEN` | Gateway | Token the calling app uses for MCP and media requests. |
| `WORKER_BEARER_TOKEN` | Both | Separate token the gateway uses to authenticate to the worker. |
| `HTTP_ADDR` | Gateway | Local bind address, default `:8080`. |
| `PUBLIC_URL` | Gateway | Advertised client-facing URL. Configure the listener or reverse proxy separately. |
| `WORKER_URL` | Gateway | Worker origin, default `http://127.0.0.1:8081`. |
| `WORKER_ADDR` | Worker | Bind address, default `127.0.0.1:8081`. |
| `DATABASE_URL` | Worker | Defaults to `sqlite:store/whatsapp-mcp.db`. PostgreSQL is also supported. |
| `TMPDIR` | Worker | Optional staging location for uploads and audio conversion. |

For remote clients, serve the gateway over HTTPS. Set `PUBLIC_URL` when using a reverse proxy. For built-in tunnelling, set `NGROK_AUTHTOKEN` and optionally `NGROK_DOMAIN` on the gateway. Use the tunnel URL printed at startup. Tunnel mode supersedes `HTTP_ADDR` and `PUBLIC_URL`.

For a native gateway using Dotvault, run `dotvault login` first, then add `--ngrok-dotvault whatsapp-mcp/ngrok/authtoken`, substituting your own full secret path and field. Use `--dotvault-config /path/to/config.yaml` for a custom config. Under systemd, put these flags in `WHATSAPP_MCP_ARGS` in the environment file.

When upgrading from the single-account standalone service, stop it before starting the worker. Reuse the same Compose project and volume, or point the native worker at the existing SQLite database. Pairing survives and existing standalone history stays in the old tables. For a GitHub-era installation, use a fresh database and link again.

## Connect and use

### Check HTTP and pair

```sh
BASE_URL=http://localhost:8080
curl -i "$BASE_URL/healthz"  # 200 with body "ok"
curl -i "$BASE_URL/mcp"      # 401 without a bearer token
```

These probes check gateway reachability and authentication. To check worker connectivity, add `$BASE_URL/mcp` to a Streamable HTTP MCP client and set `Authorization: Bearer <MCP_BEARER_TOKEN>` on each request. Connect and call `whatsapp_status`.

The remaining steps are a manual smoke test with a WhatsApp test account. Tool payloads are MCP tool arguments.

1. Call `link_whatsapp` with `{}` for a QR image. Scan it under **WhatsApp → Linked Devices → Link a Device**. Alternatively, call it with `{"phone_number":"15551234567"}`, substituting the test account's international number in digits, and enter the returned pairing code on the phone. Repeat the call to poll pairing.
2. Call `whatsapp_status` with `{}` and confirm `linked` and `connected` are both true.

### Receive and send messages

1. Call `list_chats` with `{"include_last_message":false,"sort_by":"name"}`. Copy a test chat's exact `jid`. This tool starts at page zero and defaults to 20 chats per page.
2. Call `set_subscriptions` with `{"chat_jids":["15551234567@s.whatsapp.net"]}`, substituting the copied JID. The call replaces the full selection. Use `get_subscriptions` with `{}` to confirm it.
3. Generate one new message in the selected chat and another in an unselected chat. Call `read_updates` with `{"limit":100}` and verify only the selected chat appears. Save the returned updates in the calling app before polling again with `{"cursor":"<next_cursor from the previous page>","limit":100}`. Deduplicate by chat JID and message ID. If `gap` is true, record the interruption and resume with the returned cursor.
4. Call `send_message` with `{"recipient":"15551234567@s.whatsapp.net","message":"WhatsApp MCP smoke test"}`, substituting a test recipient. Check delivery on the phone. Subscriptions control which updates enter the feed. The calling app controls send destinations.

The worker buffers up to 10,000 live messages for one hour. Restarting the gateway preserves the buffer. Restarting the worker restores pairing and subscriptions, and starts a fresh feed. The calling app owns durable conversation history.

### Transfer media

With `MCP_BEARER_TOKEN` set in your shell, upload a test file:

```sh
curl --fail-with-body \
  -H "Authorization: Bearer ${MCP_BEARER_TOKEN}" \
  -F 'file=@sample.pdf' "$BASE_URL/media/uploads"
```

Pass the returned reference to `send_file` with `{"recipient":"15551234567@s.whatsapp.net","upload_id":"<upload_id>"}`. Use the same argument shape with `send_audio_message` for a voice message. Uploads expire in 15 minutes. The limit is 32 MiB per file and 128 MiB of upload staging.

For incoming media, send an attachment into the selected chat from the phone. Read its update and call `download_media` with `{"chat_jid":"<chat_jid>","message_id":"<id>"}`. Use the returned relative `download_path` while the attachment is still in the subscribed feed:

```sh
DOWNLOAD_PATH='<download_path returned by the tool>'
curl --fail-with-body \
  -H "Authorization: Bearer ${MCP_BEARER_TOKEN}" \
  -o received-file "${BASE_URL}${DOWNLOAD_PATH}"
```

### Unlink or reset

Call `whatsapp_logout` with `{}` to unlink and clear the current feed, subscriptions, and staged uploads. Pass `{"keep_history":true}` to retain the current in-memory history and subscriptions for the same account. Use a clear logout before switching accounts. Durable history held by the calling app is managed there.

## Release checks

Run `go test ./...` and `docker compose config --quiet` with both tokens configured. Where GoReleaser is installed, run `goreleaser check`. CI also builds the binary and validates both packaged systemd units.
