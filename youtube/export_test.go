package youtube

// NewWithService exposes the unexported facade constructor to external test
// packages (youtube_test) so integration tests can build a YouTube pointed at an
// httptest server without OAuth. Production code does not use this.
var NewWithService = newWithService
