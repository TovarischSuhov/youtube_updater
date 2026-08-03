// Command youtube-updater syncs new uploads from configured YouTube channels
// into designated playlists, tracking only the last seen video per channel. It
// also manages channel→playlist pairs via --add-channel/--add-playlist,
// --remove-channel, and --list.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"youtube-updater/config"
	"youtube-updater/state"
	"youtube-updater/syncer"
	"youtube-updater/youtube"
)

// SetupLogging configures the process logger at the given severity. level is one
// of debug, info, warn, error (case-insensitive); any other value returns err.
func SetupLogging(level string) error {
	var sev slog.Level
	switch strings.ToLower(level) {
	case "debug":
		sev = slog.LevelDebug
	case "info":
		sev = slog.LevelInfo
	case "warn":
		sev = slog.LevelWarn
	case "error":
		sev = slog.LevelError
	default:
		return fmt.Errorf("invalid log level %q (want debug/info/warn/error)", level)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: sev})))
	return nil
}

// EnsureConfig guarantees a config file exists, creating an empty default on the
// first run. It returns created=true when it wrote the file.
func EnsureConfig(configPath string) (bool, error) {
	if _, err := os.Stat(configPath); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := config.SaveConfig(configPath, nil); err != nil {
		return false, err
	}
	slog.Default().Info("first run: created config.yaml", "config", configPath)
	return true, nil
}

// Run executes one sync pass end to end: load config, build the YouTube facade and
// state, run the sync, persist state (unless dryRun), and log per-channel results.
func Run(configPath, secretsPath, tokenPath, statePath, redirectURL string, dryRun bool) error {
	logger := slog.Default()

	mappings, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	yt, err := youtube.NewYouTube(secretsPath, tokenPath, redirectURL)
	if err != nil {
		return fmt.Errorf("youtube: %w", err)
	}
	st, err := state.NewState(statePath)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}

	results, syncErr := syncer.SyncAll(yt, st, mappings, dryRun)

	totalNew, totalAdded := 0, 0
	for _, r := range results {
		totalNew += r.NewCount
		totalAdded += len(r.AddedIDs)
		logger.Info("channel",
			"channel_id", r.ChannelID,
			"playlist_id", r.PlaylistID,
			"seeded", r.Seeded,
			"new_count", r.NewCount,
			"added_ids", r.AddedIDs,
			"err", r.Err,
		)
	}
	// Estimate: 1 (channels.list) + 1 (playlistItems.list) per channel, 50 per insert.
	logger.Info("sync done",
		"dry_run", dryRun,
		"channels", len(mappings),
		"new_videos", totalNew,
		"added_videos", totalAdded,
		"est_quota_units", 2*len(mappings)+50*totalAdded,
	)

	if syncErr != nil {
		return fmt.Errorf("sync: %w", syncErr)
	}
	if !dryRun {
		if err := st.Save(); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}
	return nil
}

// RunAdd adds (or updates) a channel→playlist pair: channel and playlist inputs
// accept a bare ID or a YouTube URL (@handle and /c/ / /user/ URLs are resolved
// to a channel ID via the API). It fetches the channel and playlist names,
// upserts the pair, and persists the config.
func RunAdd(configPath, secretsPath, tokenPath, redirectURL, channelInput, playlistInput string) error {
	mappings, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Parse the playlist first (pure, no API) for a fast, clear error.
	playlistID, ok := youtube.ParsePlaylistID(playlistInput)
	if !ok {
		return fmt.Errorf("parse playlist %q: expected a playlist ID or a YouTube URL with ?list=", playlistInput)
	}
	yt, err := youtube.NewYouTube(secretsPath, tokenPath, redirectURL)
	if err != nil {
		return fmt.Errorf("youtube: %w", err)
	}
	// Resolve the channel: bare IDs and /channel/ URLs need no API; @handle,
	// /c/, and /user/ slugs are resolved via the API.
	channelRef, ok := youtube.ParseChannelRef(channelInput)
	if !ok {
		return fmt.Errorf("parse channel %q: expected a channel ID or a YouTube channel/@handle/c/user URL", channelInput)
	}
	channelID, err := yt.ResolveChannelRef(channelRef)
	if err != nil {
		return fmt.Errorf("resolve channel: %w", err)
	}
	channelName, playlistName, err := yt.ResolveNames(channelID, playlistID)
	if err != nil {
		return fmt.Errorf("resolve names: %w", err)
	}
	mappings = config.AddMapping(mappings, channelID, playlistID, channelName, playlistName)
	if err := config.SaveConfig(configPath, mappings); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	slog.Default().Info("added pair",
		"channel_id", channelID, "channel_name", channelName,
		"playlist_id", playlistID, "playlist_name", playlistName,
	)
	return nil
}

// RunRemove drops the pair for the given channel and persists the config. Offline.
// The channel input may be a bare ID (matched as-is against stored config) or a
// /channel/UC… URL. Handle, custom, and user URLs are rejected — they need the
// API to resolve; run --list to find the stored channel ID.
func RunRemove(configPath, channelInput string) error {
	channelID := strings.TrimSpace(channelInput)
	if strings.Contains(channelInput, "youtube.com") || strings.Contains(channelInput, "youtu.be") {
		ref, ok := youtube.ParseChannelRef(channelInput)
		if !ok || !ref.IsID() {
			return fmt.Errorf("remove channel %q: pass the channel ID or a /channel/UC… URL (handles need the API; run --list for the ID)", channelInput)
		}
		channelID = ref.ID
	}
	mappings, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	mappings = config.RemoveMapping(mappings, channelID)
	if err := config.SaveConfig(configPath, mappings); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	slog.Default().Info("removed pair", "channel_id", channelID)
	return nil
}

// RunList prints all configured channel→playlist pairs with their names. Offline.
func RunList(configPath string) error {
	mappings, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	for _, m := range mappings {
		fmt.Printf("%s\t%s\t→\t%s\t%s\n", m.ChannelID, nameOrUnnamed(m.ChannelName), m.PlaylistID, nameOrUnnamed(m.PlaylistName))
	}
	return nil
}

func nameOrUnnamed(s string) string {
	if s == "" {
		return "<unnamed>"
	}
	return s
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to the channel→playlist config")
	secretsPath := flag.String("secrets", "client_secrets.json", "path to OAuth client_secrets.json")
	tokenPath := flag.String("token", "token.json", "path to the cached OAuth token")
	statePath := flag.String("state", "state.json", "path to the state file")
	redirectURL := flag.String("redirect", "http://localhost:8080", "loopback redirect URL for consent")
	dryRun := flag.Bool("dry-run", false, "detect only — no inserts, no state mutation")
	addChannel := flag.String("add-channel", "", "channel id to add (requires --add-playlist)")
	addPlaylist := flag.String("add-playlist", "", "playlist id to add (requires --add-channel)")
	removeChannel := flag.String("remove-channel", "", "channel id to remove")
	list := flag.Bool("list", false, "list configured channel→playlist pairs")
	logLevel := flag.String("log-level", "info", "log severity: debug/info/warn/error")
	flag.Parse()

	if err := SetupLogging(*logLevel); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	modeAdd := *addChannel != "" || *addPlaylist != ""
	modeRemove := *removeChannel != ""
	modeList := *list
	modes := 0
	if modeAdd {
		modes++
	}
	if modeRemove {
		modes++
	}
	if modeList {
		modes++
	}
	if modes > 1 {
		fmt.Fprintln(os.Stderr, "error: --add-channel/--add-playlist, --remove-channel, and --list are mutually exclusive")
		os.Exit(2)
	}
	if modeAdd && (*addChannel == "" || *addPlaylist == "") {
		fmt.Fprintln(os.Stderr, "error: --add-channel and --add-playlist are required together")
		os.Exit(2)
	}

	cmdName := "sync"
	switch {
	case modeList:
		cmdName = "list"
	case modeRemove:
		cmdName = "remove"
	case modeAdd:
		cmdName = "add"
	}
	attrs := []any{"command", cmdName, "config", *configPath}
	switch cmdName {
	case "sync":
		attrs = append(attrs, "secrets", *secretsPath, "state", *statePath)
	case "add":
		attrs = append(attrs, "secrets", *secretsPath)
	}
	slog.Default().Info("start", attrs...)

	if _, err := EnsureConfig(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	var err error
	switch {
	case modeList:
		err = RunList(*configPath)
	case modeRemove:
		err = RunRemove(*configPath, *removeChannel)
	case modeAdd:
		err = RunAdd(*configPath, *secretsPath, *tokenPath, *redirectURL, *addChannel, *addPlaylist)
	default:
		err = Run(*configPath, *secretsPath, *tokenPath, *statePath, *redirectURL, *dryRun)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
