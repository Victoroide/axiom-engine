# Configuration

Process configuration is parsed at startup in `internal/config/config.go`. The service fails immediately during initialization if any variable violates format constraints, boundaries, or relational rules.

This document is the authoritative reference for all environment variables supported by AxiomEngine.

---

## Environment Variables

| Variable | Type | Default | Accepted Values | Effect |
|---|---|---|---|---|
| `PORT` | Integer | `8080` | `1` to `65535` | TCP port on which the HTTP server listens. Configured in `internal/config/config.go`. |
| `AXIOM_AUTH_DISABLED` | Boolean | `false` | `true` or `false` | When `true`, disables token authentication on `/api/v1/*` routes and logs a startup warning `auth.disabled`. When `false`, `AXIOM_INTERNAL_TOKEN` is required. Configured in `internal/config/config.go`. |
| `AXIOM_INTERNAL_TOKEN` | String | `""` (unset) | At least 32 visible ASCII characters (`0x21`–`0x7E`) without whitespace | Shared secret token required in `X-Internal-Token` or `Authorization: Bearer` on `/api/v1/*` routes. Required at startup unless `AXIOM_AUTH_DISABLED=true`. Non-empty tokens must satisfy length and character rules even when authentication is disabled. Tokens are not trimmed. Configured in `internal/config/config.go`. |
| `CORS_ALLOWED_ORIGINS` | String | `""` (unset) | `*` alone, or comma-separated list of `http://` or `https://` URLs | Configures allowed origins for cross-origin requests. URLs must not include paths, query parameters, or fragments. When unset or empty, CORS middleware is disabled. Configured in `internal/config/config.go`. |
| `RATE_LIMIT_MAX` | Integer | `50` | Positive integer (`> 0`) | Maximum number of requests allowed from a client IP within `RATE_LIMIT_WINDOW`. Exceeding requests receive HTTP 429. Configured in `internal/config/config.go`. |
| `RATE_LIMIT_WINDOW` | Duration | `1m` | Positive Go duration string (e.g. `30s`, `1m`, `5m`) | Sliding window duration for IP-based rate limiting. Configured in `internal/config/config.go`. |
| `TRUSTED_PROXIES` | String | `""` (unset) | Comma-separated list of IPv4/IPv6 addresses or CIDRs | Proxies trusted by Fiber to extract the client IP from `X-Forwarded-For`. When unset, proxy headers are ignored. Configured in `internal/config/config.go`. |
| `REQUEST_TIMEOUT` | Duration | `20s` | Positive Go duration string | Total timeout deadline for servicing incoming HTTP requests in the reasoning pipeline. Must be greater than or equal to `LLM_TIMEOUT`. Configured in `internal/config/config.go`. |
| `LLM_TIMEOUT` | Duration | `15s` | Positive Go duration string | Timeout applied to each individual per-topic model call to Amazon Bedrock. Must not exceed `REQUEST_TIMEOUT`. Configured in `internal/config/config.go`. |
| `MAX_FAILED_TOPICS` | Integer | `10` | Positive integer (`> 0`) | Maximum number of unique failed topics accepted in a single adaptive plan or cognitive graph request. Configured in `internal/config/config.go`. |
| `GRAPH_FILE` | String | `""` (unset) | Path to readable JSON file | Custom curriculum knowledge graph file. When unset or empty, the service loads the embedded seed graph from `internal/graph/seed.json`. Configured in `internal/graph/loader.go`. |
| `LOG_LEVEL` | String | `info` | `debug`, `info`, `warn`, `error` (case-insensitive) | Minimum log level for structured JSON logging emitted to standard output via `log/slog`. Configured in `internal/config/config.go`. |
| `SHUTDOWN_TIMEOUT` | Duration | `10s` | Positive Go duration string | Maximum duration allowed for draining in-flight HTTP connections during graceful shutdown on SIGINT or SIGTERM. Configured in `internal/config/config.go`. |
| `AWS_REGION` | String | `""` | Valid AWS region identifier (e.g. `us-east-1`) | AWS region where Amazon Bedrock runtime calls are dispatched. Evaluated by the underlying AWS SDK. |
| `AWS_ACCESS_KEY_ID` | String | `""` | IAM access key ID | Long-term IAM access key ID with `bedrock:InvokeModel` permissions. |
| `AWS_SECRET_ACCESS_KEY` | String | `""` | IAM secret access key | Secret access key corresponding to `AWS_ACCESS_KEY_ID`. |
| `AWS_SESSION_TOKEN` | String | `""` | STS session token string | Temporary session token when using assumed roles or temporary STS credentials. |
| `AWS_PROFILE` | String | `""` | Named profile string | Selects an AWS profile configured in `~/.aws/credentials` or `~/.aws/config`. |
| `BAML_LOG` | String | `""` (`off` in container) | `off`, `warn`, `info`, `debug` | Controls native BAML logging. Set to `off` to prevent prompt text from being logged to standard output. |
| `BAML_LIBRARY_PATH` | String | `""` (`/app/libbaml_cffi.so` in container) | File path to dynamic shared library | Filesystem path to the compiled BAML CFFI engine binary. Set in `Dockerfile`. |
| `BAML_LIBRARY_DISABLE_DOWNLOAD` | Boolean | `false` (`true` in container) | `true` or `false` | When true, prevents the BAML runtime from attempting to download shared libraries from the internet at runtime. Set in `Dockerfile`. |

---

## Derived Transport Timeouts

Transport-level deadlines are derived from configuration in `internal/config/config.go` and enforced on the Fiber application (`internal/server/server.go`) without requiring separate environment variables:

- **Read Timeout**: `10s` (`fiber.Config.ReadTimeout`). Bounds reading of request headers and body.
- **Write Timeout**: Derived as `REQUEST_TIMEOUT + 10s` (default `30s`, `fiber.Config.WriteTimeout`). Bounds response delivery. Config loading rejects `REQUEST_TIMEOUT` values that would cause duration overflow.
- **Idle Timeout**: `1m` (`fiber.Config.IdleTimeout`). Bounds keep-alive connections.

---

## AWS Credential Resolution

Amazon Bedrock calls executed through BAML rely on the standard AWS credential provider chain. For detailed provider behavior and precedence, refer to the [AWS SDK Documentation](https://docs.aws.amazon.com/sdk-for-go/v1/developer-guide/configuring-sdk.html#specifying-credentials).

The runtime resolves credentials in the following standard order:
1. Environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and optional `AWS_SESSION_TOKEN`).
2. Shared configuration and credentials files (`~/.aws/credentials` and `~/.aws/config` selected by `AWS_PROFILE`).
3. Web identity token (OIDC / IAM Roles for Service Accounts).
4. ECS / Container metadata credentials (`AWS_CONTAINER_CREDENTIALS_RELATIVE_URI` or `AWS_CONTAINER_CREDENTIALS_FULL_URI`).
5. EC2 Instance Profile (IMDSv2).

### Required IAM Permissions

The IAM principal executing AxiomEngine requires permissions to invoke Amazon Bedrock foundation models:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "BedrockInvokeModel",
      "Effect": "Allow",
      "Action": [
        "bedrock:InvokeModel"
      ],
      "Resource": "arn:aws:bedrock:*::foundation-model/amazon.nova-micro-v1:0"
    }
  ]
}
```
