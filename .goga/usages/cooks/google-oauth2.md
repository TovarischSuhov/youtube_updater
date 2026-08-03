# Google OAuth2 Desktop Flow (Go)

## Domain

This usage describes how to obtain **OAuth2 user credentials** in a Go desktop /
CLI program and cache the resulting token locally, using
`golang.org/x/oauth2` and `golang.org/x/oauth2/google`. It is the credential
foundation for any Google API call that acts on behalf of a user — here, writing
to the user's YouTube playlists.

**Audience:** the implementing agent and any developer setting up the auth flow.
This file is self-contained: it produces an authenticated `*http.Client` that the
rest of the program feeds into any Google service client.

## Why OAuth2 (and not an API key / service account)

YouTube playlist writes require **user consent**. An API key is read-only, and
service accounts are not usable for YouTube playlist operations on a personal
account. A one-time interactive consent is therefore mandatory; after that the
cached token (with refresh) keeps the script working unattended.

## Prerequisites

1. In **Google Cloud Console**: create a project → enable the **YouTube Data API
   v3** → create an OAuth client of type **Desktop app** → download
   `client_secrets.json`.
2. The downloaded file has a top-level `"installed"` key — that is the shape
   `google.ConfigFromJSON` expects.
3. Authorize a loopback redirect for the consent callback (Desktop clients permit
   `http://localhost:<port>`).

```bash
go get golang.org/x/oauth2
go get golang.org/x/oauth2/google
```

The scope for playlist read/write is `https://www.googleapis.com/auth/youtube`.

## Scenario 1 — Load the OAuth2 config from client_secrets.json

```go
import (
    "golang.org/x/oauth2"
    "golang.org/x/oauth2/google"
)

const youtubeScope = "https://www.googleapis.com/auth/youtube"

// configFromSecrets reads client_secrets.json and returns an oauth2.Config.
func configFromSecrets(path, redirectURL string) (*oauth2.Config, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read client secrets: %w", err)
    }
    config, err := google.ConfigFromJSON(b, youtubeScope)
    if err != nil {
        return nil, fmt.Errorf("parse client secrets: %w", err)
    }
    config.RedirectURL = redirectURL // e.g. "http://localhost:8080"
    return config, nil
}
```

## Scenario 2 — Interactive consent on first run (local callback)

On first run there is no cached token. Start a local HTTP server, send the user
to the consent URL, and exchange the returned code for a token. Request
`AccessTypeOffline` so the token includes a **refresh token** — required for
long-lived unattended use.

```go
// authorize runs the consent flow and returns the granted token.
func authorize(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
    const port = "8080"
    state := randomState() // crypto/rand, opaque to Google

    authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

    codeCh := make(chan string, 1)
    errCh := make(chan error, 1)
    mux := http.NewServeMux()
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        q := r.URL.Query()
        if q.Get("state") != state {
            errCh <- fmt.Errorf("oauth state mismatch")
            http.Error(w, "state mismatch", http.StatusBadRequest)
            return
        }
        if e := q.Get("error"); e != "" {
            errCh <- fmt.Errorf("oauth error: %s", e)
            http.Error(w, e, http.StatusBadRequest)
            return
        }
        codeCh <- q.Get("code")
        fmt.Fprintln(w, "Authorization complete. You can close this tab.")
    })
    srv := &http.Server{Addr: ":" + port, Handler: mux}
    go func() { _ = srv.ListenAndServe() }()
    defer srv.Shutdown(ctx)

    log.Printf("Open this URL to authorize (once):\n  %s\n", authURL)
    // Optionally auto-open the URL via the OS browser; printing is portable.

    select {
    case code := <-codeCh:
        return config.Exchange(ctx, code) // offline => refresh token included
    case err := <-errCh:
        return nil, err
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
```

`oauth2.ApprovalForce` forces the consent screen so a refresh token is granted
even if the user previously consented without `AccessTypeOffline`. Use it on the
first run only.

## Scenario 3 — Cache the token and auto-refresh on later runs

Persist the token to a local file (mode `0600`). On later runs, load it and build
the HTTP client from it — `config.Client` wraps a token source that **refreshes
expired access tokens automatically** using the stored refresh token.

```go
// loadToken reads and validates a cached token.
func loadToken(path string) (*oauth2.Token, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        return nil, err
    }
    var tok oauth2.Token
    if err := json.Unmarshal(b, &tok); err != nil {
        return nil, err
    }
    return &tok, nil
}

// saveToken writes the token with restrictive permissions.
func saveToken(path string, tok *oauth2.Token) error {
    b, err := json.MarshalIndent(tok, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(path, b, 0600)
}
```

### Non-interactive runs: don't reject a refreshable token

A cached access token expires after ~1 hour, so in an unattended run (cron, CI)
the stored access token is almost always expired at start time — but the **refresh
token is long-lived**. `oauth2.Token.Valid()` checks only the access token, so
gating the load on it (`if !tok.Valid() { return err }`) wrongly rejects a
refreshable token and falls back to the interactive consent flow, which cannot
complete headless. Accept a stored token whenever it still carries a refresh
token and let `config.TokenSource` mint a fresh access token on demand:

```go
if tok.RefreshToken == "" && !tok.Valid() {
    return nil, errors.New("stored token invalid")
}
return &tok, nil
```

The interactive consent flow then runs only on the true first run, or after the
refresh token is revoked — keeping scheduled runs fully unattended.

### Token acquisition (cache-first), then build the client

```go
// tokenFor returns a usable token: cached if present, otherwise via consent flow.
func tokenFor(ctx context.Context, config *oauth2.Config, tokenPath string) (*oauth2.Token, error) {
    if tok, err := loadToken(tokenPath); err == nil {
        return tok, nil // refresh happens transparently in the client
    }
    tok, err := authorize(ctx, config) // first run only
    if err != nil {
        return nil, err
    }
    if err := saveToken(tokenPath, tok); err != nil {
        return nil, fmt.Errorf("persist token: %w", err)
    }
    return tok, nil
}
```

### Persisting refreshed tokens

`config.Client(ctx, tok)` refreshes expired access tokens in memory but does
**not** write the refreshed token back to disk. To keep the cache fresh (and
survive refresh-token rotation), wrap the source so it saves on refresh:

```go
type savingTokenSource struct {
    base  oauth2.TokenSource
    path  string
    mtx   sync.Mutex
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
    s.mtx.Lock()
    defer s.mtx.Unlock()
    tok, err := s.base.Token()
    if err != nil {
        return nil, err
    }
    _ = saveToken(s.path, tok) // best-effort
    return tok, nil
}

// httpClient builds an OAuth2-backed client that also persists refreshed tokens.
func httpClient(ctx context.Context, config *oauth2.Config, tok *oauth2.Token, tokenPath string) *http.Client {
    base := config.TokenSource(ctx, tok)          // refreshes automatically
    src := &savingTokenSource{base: base, path: tokenPath}
    return oauth2.NewClient(ctx, src)
}
```

Feed the returned `*http.Client` into any Google service constructor.

## Constraints & notes

- **Refresh tokens can be revoked** (user revokes access, or 6 months of
  inactivity). Detect a refresh failure and re-run the consent flow (`authorize`).
- **Do not commit secrets.** Keep `client_secrets.json` and the cached token file
  out of version control and on disk with mode `0600`.
- **PKCE** (`oauth2.GenerateVerifier()` + `oauth2.VerifierOption(v)` on both
  `AuthCodeURL` and `Exchange`) is recommended hardening for the loopback flow.
- **Headless / cron runs** require that the first run happened interactively so a
  cached token with a refresh token exists; subsequent runs never need a browser.
