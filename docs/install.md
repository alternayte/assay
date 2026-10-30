# Install and operate Assay

Assay runs with Docker Compose on one machine. The Compose file has two services: `db` (PostgreSQL 17 with pgvector) and `assay`. The `demo` profile adds `fixtures`, the server of the synthetic Acme sources.

## Install

You need Docker Desktop with Compose v2. Run:

```sh
curl -fsSL https://github.com/alternayte/assay/releases/latest/download/install.sh | sh
```

The script does these steps:

1. It checks Docker, Compose v2 and the Docker daemon. If one is missing, it stops and names Docker Desktop.
2. It downloads `compose.yaml` and `env.example` from the release.
3. On the first run only, it creates `.env` with a new secret key, a database password and a setup token.
4. It pulls the image and starts the services. Assay binds to `127.0.0.1` only.
5. It waits until `/readyz` answers, then prints the URL and the setup token.

Open `/setup` and create the first admin with the setup token. Then add connections in Settings, Connections.

### Flags

| Flag | Use |
|---|---|
| `--dir DIR` | The install directory. Default: `~/.assay`. |
| `--port PORT` | The port on `127.0.0.1`. Default: `8080`. |
| `--version VER` | A release tag, such as `v1.0.0`. Default: the newest release. |
| `--demo` | Also start the Acme fixture server. After setup, Assay creates the Acme connections. |
| `--down` | Stop Assay. The data stays. |

To pass flags through the pipe, use `sh -s --`, for example `... | sh -s -- --port 9090`.

`ASSAY_IMAGE` selects another image, for example a local build. `ASSAY_INSTALL_SRC` reads `compose.yaml` and `env.example` from a directory or URL instead of the release.

### The demo

With `--demo`, setup creates five connections to the fixture server:

- `acme-engineering`: the Confluence space ENG, shared with Everyone.
- `acme-restricted`: the Confluence space RST, visible to the first admin only. Its pages hold the canary word `CANARY-7Q4Z`. No other user sees it.
- `acme-jira`: the Jira projects PAY and REM, shared with Everyone.
- `acme-github`: two GitHub repositories, shared with Everyone.
- `acme-azdo`: one Azure DevOps Server repository, shared with Everyone.

The fixture server also has a fake model API. To try answers and page summaries with no real model, add a model endpoint in Settings, Models, with the URL `http://fixtures:8090/v1` and the key `fake-key`.

## Update

Run the install command again. The script keeps `.env` and the data, pulls the new image and restarts Assay. Assay applies the database migrations at start.

## Stop

```sh
sh ~/.assay/install.sh --down --dir ~/.assay
```

Or run `docker compose down` in the install directory. The data stays in the Docker volume `db`.

## Back up and restore

Back up the database and `.env`. Without the secret key in `.env`, Assay cannot read the stored tokens.

```sh
cd ~/.assay
docker compose exec -T db pg_dump -U assay -Fc assay > assay.dump
cp .env env.backup
```

To restore into an empty install:

```sh
cd ~/.assay
docker compose stop assay
docker compose exec -T db pg_restore -U assay -d assay --clean --if-exists < assay.dump
docker compose start assay
```

## Corporate proxy and certificates

Set `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` in `.env`, or export them before the first run of the script. `db` and `fixtures` must stay in `NO_PROXY`. All outbound requests of Assay use these settings.

For a proxy that inspects TLS, put the CA certificates in `ASSAY_EXTRA_CA_PEM` (PEM text) or in a file that `ASSAY_EXTRA_CA_FILE` names.

## Checks and logs

- `/healthz` tells that the process runs. `/readyz` tells that the database answers and the migrations are current. `/metrics` has Prometheus metrics.
- `docker compose exec assay assay doctor` checks the database version and extensions, the secret key, the proxy, the CA certificates, and the reachability and TLS of each connection and model endpoint. Settings, Diagnostics runs the same checks.
- Logs are JSON. They never hold `Authorization` headers, tokens or secrets.
- Set `OTEL_EXPORTER_OTLP_ENDPOINT` to export traces with OpenTelemetry.

## Roles and replicas

`ASSAY_ROLE` selects the parts of the process: `web` serves the UI, REST and MCP; `worker` runs the sync and model jobs; `all` runs both. Several replicas of each role are safe.

## Rotate the secret key

1. Set `ASSAY_SECRET_KEY_NEW` to a new key: 32 random bytes in base64.
2. Run `docker compose exec assay assay secrets rekey`.
3. Set `ASSAY_SECRET_KEY` to the new key, remove `ASSAY_SECRET_KEY_NEW` and restart.
