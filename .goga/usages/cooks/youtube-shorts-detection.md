# YouTube Shorts Detection (Go)

## Domain

This usage describes how to detect whether a YouTube video is a **Short** from Go.
The YouTube Data API v3 has no `isShort` field (Google Issue Tracker #232112727,
still open), so the only reliable detection is an **unauthenticated HEAD probe** of
the public web endpoint `https://www.youtube.com/shorts/{videoId}`. This file covers
exactly that probe and the duration-based fallback used when the probe fails.

**Audience:** the implementing agent and any developer working in the `youtube` cell.
This is NOT the Data API — it uses a plain `*http.Client` with no credentials, and it
is unofficial: YouTube may change the endpoint's behaviour without notice. See
`youtube-data-api` for the authenticated Data API calls used alongside this.

## Prerequisites

No module dependencies — standard library only:

```go
import (
    "context"
    "fmt"
    "net/http"
    "time"
)
```

## The redirect trap (read first)

The HEAD client must **not follow redirects**. For a non-Short,
`youtube.com/shorts/{id}` responds with a **3xx redirect** to `/watch?v={id}`;
following it lands on a `200`, which would wrongly classify every regular video as a
Short. A Short responds with an immediate **200** on `/shorts/{id}` itself.

```go
// A client that does NOT follow redirects — a regular video's /shorts/{id} 3xx
// redirects to /watch, which 200s; following it would read every regular video
// as a Short.
shortsHTTP := &http.Client{
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        return http.ErrUseLastResponse
    },
    Timeout: 10 * time.Second,
}
```

## Why non-200 is not a "regular video" signal

Treat every non-`200` response as **inconclusive** — never as evidence the video
is regular. The `/shorts/{id}` endpoint is unofficial and returns non-`200` for
reasons unrelated to Short-ness:

- **Consent / region redirect (`3xx`)** — `youtube.com` may answer with a `302` to
  `consent.google.com` or a locale domain depending on the caller's IP/region.
- **Bot defense / rate limiting (`403`, `429`)** — repeated unauthenticated HEAD
  requests from a datacenter IP (e.g. a CI runner) are routinely blocked.

A real Short answers `200`; a real non-Short typically `3xx`-redirects to
`/watch`. But a blocked or consent response is indistinguishable from a genuine
redirect at the status-code level, so the only safe reading is: `200` ⇒ Short,
anything else ⇒ fall back to duration. Reading a non-`200` as "regular" lets real
Shorts leak through whenever the probe is blocked.

## Scenario 1 — Probe whether a video is a Short

HEAD `/shorts/{id}`. Interpret the immediate (pre-redirect) status. **Only `200`
is a definitive Short signal**; every other outcome is inconclusive and the caller
MUST fall back to the duration heuristic (Scenario 2):

| Immediate status          | Meaning      |
|---------------------------|--------------|
| `200`                     | Short        |
| `3xx`, `4xx`, `5xx`       | inconclusive |
| transport error / timeout | inconclusive |

```go
// isShort reports whether id is a Short. err != nil means the probe was
// inconclusive (ANY non-200 status, or a transport error) — the caller must
// fall back to a heuristic. Only a 200 conclusively identifies a Short.
func isShort(ctx context.Context, httpc *http.Client, base, id string) (bool, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodHead, base+"/shorts/"+id, nil)
    if err != nil {
        return false, err
    }
    resp, err := httpc.Do(req)
    if err != nil {
        return false, err
    }
    defer resp.Body.Close()
    if resp.StatusCode == http.StatusOK {
        return true, nil
    }
    return false, fmt.Errorf("youtube: shorts probe inconclusive (status %d)", resp.StatusCode)
}
```

## Scenario 2 — Duration fallback

When the probe is inconclusive — any non-`200` status or a transport error (see
Scenario 1) — classify by `contentDetails.duration` (parsed from ISO 8601 via the
Data API, see `youtube-data-api`): a video **≤ 180s** is treated as a Short. 180s is
the current Shorts maximum. This is coarse — it cannot tell a Short
from a genuinely short regular video — so it is a safety net, not the primary signal.

## Reliability caveats

- **Unofficial endpoint.** Behaviour can change; the duration fallback keeps sync
  working if the probe semantics shift or the endpoint is blocked.
- **Rate limiting.** One HEAD per candidate video. New-since-watermark volume is
  normally small; if it grows, add a small concurrency cap or backoff.
- **No auth, no quota.** The probe costs no Data API quota units.
