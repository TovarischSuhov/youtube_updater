package youtube

// NewWithService exposes the unexported facade constructor to external test
// packages (youtube_test) so integration tests can build a YouTube pointed at an
// httptest server without OAuth. Production code does not use this.
var NewWithService = newWithService

// NewWithShorts exposes the unexported Shorts-probe constructor so external test
// packages can build a YouTube whose probe targets an httptest server. Production
// code does not use this.
var NewWithShorts = newWithShorts
