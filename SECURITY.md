# Security Policy

## Supported versions

Security fixes land on the latest release track (`v2.x`). Older releases are not maintained.

## Reporting a vulnerability

Please **do not open a public issue** for a security problem.

Use GitHub's private vulnerability reporting: open the **Security** tab of this repository and choose **Report a vulnerability**. That channel is visible only to the maintainers.

A useful report includes:

- the affected version, tag or commit;
- what the issue is and which component it affects (proxy, monitor, MCP, storage, recording format);
- reproduction steps or a minimal config;
- the impact as you understand it, and any suggested fix.

We aim to acknowledge a report within a few days, agree on a disclosure timeline with you, and credit you in the release notes unless you prefer otherwise.

## Operator notes

Trajecta is local-first, but it handles real credentials and real conversation content, so a deployment still needs care:

- **Cassettes store raw traffic.** With the default `debug.mask_key: true` the recorder replaces the `Authorization` value with `Bearer fake-key-logging` and the `api-key` / `x-api-key` / `x-goog-api-key` values with `fake-key-logging` in the recorded *request* headers before writing. Every other header, the request body, and the whole response are stored verbatim — that is what makes replay faithful. If you set `debug.mask_key: false`, credentials are written to disk in plaintext. Either way, treat `trace.output_dir` / `TRAJECTA_OUTPUT_DIR` as sensitive and do not commit recordings of production traffic.
- **URLs and DSNs are redacted before they are stored or printed.** A URL that reaches a cassette, the `logs` table, a log line or `config inspect` has the password in its userinfo replaced, every query parameter whose *name* matches the shared marker list (`key`, `token`, `secret`, `password`, `passwd`, `credential`, `signature`, `sig`, `access_token`, `api_key`, `authorization`, `auth`) replaced with a placeholder, and a userinfo *username* replaced when it matches that same list. Database DSNs are redacted in both shapes: the URL form covers the userinfo and the query string, and the keyword form covers every `key=value` field. The residual is a userinfo username that names no secret, such as the literal API key in `https://sk-live-abc@gateway.example.com/v1`: the username is kept because it identifies the upstream, so do not configure an upstream that way — put the credential in the channel API key, which is encrypted. `debug.mask_key: false` does not disable this redaction.
- **Channel credentials are encrypted at rest.** API keys and secret headers are sealed with AES-256-GCM under a random 32-byte local key stored at `<output_dir>/trace_index.secret` (mode `0600`). Back that file up together with the application database: without it the stored channel credentials cannot be decrypted.
- **Change the default Compose credentials.** `POSTGRES_PASSWORD` in `.env.example` is an example value; the tracked `config/config.yaml` intentionally contains no provider keys.
- **Do not expose the monitor port to untrusted networks.** The monitor, the proxy API and the MCP endpoint all authenticate (login session or a personal `Bearer` token), and the monitor is where provider credentials and recorded traffic are readable. Keep it on localhost or behind your own access control.
- **Personal tokens are hashed, not recoverable.** Treat a leaked token as compromised and revoke it in the monitor; it cannot be looked up from the database.

