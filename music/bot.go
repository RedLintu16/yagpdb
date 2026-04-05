package music

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/botlabs-gg/yagpdb/v2/commands"
	"github.com/botlabs-gg/yagpdb/v2/lib/dcmd"
)

func (p *Plugin) AddCommands() {
	commands.AddRootCommands(p,
		&commands.YAGCommand{
			CmdCategory: commands.CategoryFun,
			Name:        "play",
			Description: "Play audio from a YouTube or SoundCloud URL (playlists supported)",
			Arguments: []*dcmd.ArgDef{
				{Name: "URL", Type: dcmd.String, Help: "YouTube or SoundCloud URL"},
			},
			RequiredArgs:        1,
			SlashCommandEnabled: true,
			DefaultEnabled:      true,
			RunFunc: func(data *dcmd.Data) (interface{}, error) {
				url := data.Args[0].Str()

				if !isValidMusicURL(url) {
					return "Please provide a valid YouTube or SoundCloud URL", nil
				}

				var voiceChannel int64
				vs := data.GuildData.GS.GetVoiceState(data.Author.ID)
				if vs != nil {
					voiceChannel = vs.ChannelID
				}
				if voiceChannel == 0 {
					return "You're not in a voice channel", nil
				}

				if isPlaylistURL(url) {
					tracks, err := expandPlaylist(url)
					if err != nil {
						return "Failed to load playlist: `" + err.Error() + "`", nil
					}
					if len(tracks) == 0 {
						return "Playlist appears to be empty", nil
					}
					for _, t := range tracks {
						RequestPlay(data.GuildData.GS.ID, voiceChannel, data.ChannelID, t.URL, t.Title, t.Uploader)
					}
					return fmt.Sprintf("Queued %d tracks", len(tracks)), nil
				}

				title, uploader := fetchTrackInfo(url)
				queued := RequestPlay(data.GuildData.GS.ID, voiceChannel, data.ChannelID, url, title, uploader)

				display := (&PlayRequest{URL: url, Title: title, Uploader: uploader}).DisplayName()
				if queued {
					return "Queued: " + display, nil
				}
				return "Playing: " + display, nil
			},
		},

		&commands.YAGCommand{
			CmdCategory: commands.CategoryFun,
			Name:        "search",
			Aliases:     []string{"search", "f"},
			Description: "Search YouTube and SoundCloud for a song",
			Arguments: []*dcmd.ArgDef{
				{Name: "Query", Type: dcmd.String, Help: "Song name to search for"},
			},
			RequiredArgs:        1,
			SlashCommandEnabled: true,
			DefaultEnabled:      true,
			RunFunc: func(data *dcmd.Data) (interface{}, error) {
				// For prefix/mention commands, use the full remaining text.
				// For slash commands, the arg contains the full string already.
				query := data.Args[0].Str()
				if data.TraditionalTriggerData != nil {
					query = strings.TrimSpace(data.TraditionalTriggerData.MessageStrippedPrefix)
				}

				type searchResult struct {
					source  string
					tracks  []trackInfo
				}

				ytCh := make(chan searchResult, 1)
				scCh := make(chan searchResult, 1)

				go func() {
					tracks, _ := searchTracks("ytsearch3:"+query)
					ytCh <- searchResult{"YouTube", tracks}
				}()
				go func() {
					tracks, _ := searchTracks("scsearch3:"+query)
					scCh <- searchResult{"SoundCloud", tracks}
				}()

				yt := <-ytCh
				sc := <-scCh

				if len(yt.tracks) == 0 && len(sc.tracks) == 0 {
					return "No results found", nil
				}

				// Combine into one numbered list
				var all []trackInfo
				out := ""
				n := 1
				for _, res := range []searchResult{yt, sc} {
					if len(res.tracks) == 0 {
						continue
					}
					out += fmt.Sprintf("**%s**\n", res.source)
					for _, t := range res.tracks {
						out += fmt.Sprintf("%d. %s\n", n, (&PlayRequest{Title: t.Title, Uploader: t.Uploader, URL: t.URL}).DisplayName())
						all = append(all, t)
						n++
					}
					out += "\n"
				}
				out += "Reply with a number to play"

				storePendingSearch(data.GuildData.GS.ID, data.Author.ID, data.ChannelID, all)
				return out, nil
			},
		},

		&commands.YAGCommand{
			CmdCategory:         commands.CategoryFun,
			Name:                "queue",
			Aliases:             []string{"queue", "q"},
			Description:         "Show the current queue",
			SlashCommandEnabled: true,
			DefaultEnabled:      true,
			RunFunc: func(data *dcmd.Data) (interface{}, error) {
				return getQueue(data.GuildData.GS.ID), nil
			},
		},

		&commands.YAGCommand{
			CmdCategory:         commands.CategoryFun,
			Name:                "skip",
			Aliases:             []string{"skip", "s"},
			Description:         "Skip the current track",
			SlashCommandEnabled: true,
			DefaultEnabled:      true,
			RunFunc: func(data *dcmd.Data) (interface{}, error) {
				if msg := skipPlayer(data.GuildData.GS.ID); msg != "" {
					return msg, nil
				}
				return "Skipped", nil
			},
		},

		&commands.YAGCommand{
			CmdCategory:         commands.CategoryFun,
			Name:                "back",
			Aliases:             []string{"back", "b"},
			Description:         "Go back to the previous track",
			SlashCommandEnabled: true,
			DefaultEnabled:      true,
			RunFunc: func(data *dcmd.Data) (interface{}, error) {
				if msg := backPlayer(data.GuildData.GS.ID); msg != "" {
					return msg, nil
				}
				return "Going back", nil
			},
		},

		&commands.YAGCommand{
			CmdCategory:         commands.CategoryFun,
			Name:                "stop",
			Aliases:             []string{"stop", "n"},
			Description:         "Stop playback and leave the voice channel",
			SlashCommandEnabled: true,
			DefaultEnabled:      true,
			RunFunc: func(data *dcmd.Data) (interface{}, error) {
				if msg := stopPlayer(data.GuildData.GS.ID); msg != "" {
					return msg, nil
				}
				return "Stopped", nil
			},
		},
	)
}

func isValidMusicURL(url string) bool {
	return strings.Contains(url, "youtube.com") ||
		strings.Contains(url, "youtu.be") ||
		strings.Contains(url, "soundcloud.com")
}

func isPlaylistURL(url string) bool {
	if strings.Contains(url, "soundcloud.com") && strings.Contains(url, "/sets/") {
		return true
	}
	if strings.Contains(url, "youtube.com") && strings.Contains(url, "list=") {
		return true
	}
	return false
}

type trackInfo struct {
	URL      string
	Title    string
	Uploader string
}

// fetchTrackInfo retrieves the title and uploader for a single track URL.
func fetchTrackInfo(url string) (title, uploader string) {
	out, err := exec.Command("yt-dlp",
		"--no-playlist",
		"--print", "%(title)s|||%(uploader)s",
		"--no-warnings",
		url,
	).Output()
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|||", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(string(out)), ""
}

// searchTracks searches using a yt-dlp search prefix (e.g. "ytsearch3:query").
func searchTracks(searchQuery string) ([]trackInfo, error) {
	logger.Infof("searchTracks: running yt-dlp with query: %q", searchQuery)
	cmd := exec.Command("yt-dlp",
		"--flat-playlist",
		"--print", "%(url)s|||%(title)s|||%(uploader)s",
		"--no-warnings",
		searchQuery,
	)
	out, err := cmd.CombinedOutput()
	logger.Infof("searchTracks: yt-dlp output: %s", string(out))
	if err != nil {
		logger.WithError(err).Errorf("searchTracks: yt-dlp failed for query %q", searchQuery)
		return nil, err
	}

	var tracks []trackInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|||", 3)
		t := trackInfo{URL: parts[0]}
		if len(parts) >= 2 {
			t.Title = strings.TrimSpace(parts[1])
		}
		if len(parts) >= 3 {
			t.Uploader = strings.TrimSpace(parts[2])
		}
		tracks = append(tracks, t)
	}
	return tracks, nil
}

// expandPlaylist returns all tracks in a playlist with their titles and uploaders.
func expandPlaylist(url string) ([]trackInfo, error) {
	out, err := exec.Command("yt-dlp",
		"--flat-playlist",
		"--print", "%(url)s|||%(title)s|||%(uploader)s",
		"--no-warnings",
		url,
	).Output()
	if err != nil {
		return nil, err
	}

	var tracks []trackInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|||", 3)
		t := trackInfo{URL: parts[0]}
		if len(parts) >= 2 {
			t.Title = strings.TrimSpace(parts[1])
		}
		if len(parts) >= 3 {
			t.Uploader = strings.TrimSpace(parts[2])
		}
		tracks = append(tracks, t)
	}
	return tracks, nil
}
