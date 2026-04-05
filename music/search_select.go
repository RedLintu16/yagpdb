package music

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/bot/eventsystem"
	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
)


type pendingSearch struct {
	tracks    []trackInfo
	channelID int64
	guildID   int64
	expiresAt time.Time
}

var (
	pendingSearches   = make(map[string]*pendingSearch)
	pendingSearchesMu sync.Mutex
)

func pendingKey(guildID, userID int64) string {
	return fmt.Sprintf("%d:%d", guildID, userID)
}

// storePendingSearch saves search results for a user, expiring after 60s.
func storePendingSearch(guildID, userID, channelID int64, tracks []trackInfo) {
	key := pendingKey(guildID, userID)
	pendingSearchesMu.Lock()
	pendingSearches[key] = &pendingSearch{
		tracks:    tracks,
		channelID: channelID,
		guildID:   guildID,
		expiresAt: time.Now().Add(60 * time.Second),
	}
	pendingSearchesMu.Unlock()
}

func popPendingSearch(guildID, userID int64) *pendingSearch {
	key := pendingKey(guildID, userID)
	pendingSearchesMu.Lock()
	defer pendingSearchesMu.Unlock()
	ps, ok := pendingSearches[key]
	if !ok || time.Now().After(ps.expiresAt) {
		delete(pendingSearches, key)
		return nil
	}
	delete(pendingSearches, key)
	return ps
}

func handleMessageCreate(evt *eventsystem.EventData) {
	msg := evt.EvtInterface.(*discordgo.MessageCreate)

	// Ignore bots and empty messages
	if msg.Author == nil || msg.Author.Bot {
		return
	}

	content := strings.TrimSpace(msg.Content)
	n, err := strconv.Atoi(content)
	if err != nil || n < 1 {
		return
	}

	guildID := msg.GuildID
	if guildID == 0 {
		return
	}

	ps := popPendingSearch(guildID, msg.Author.ID)
	if ps == nil {
		return
	}

	if n > len(ps.tracks) {
		common.BotSession.ChannelMessageSend(msg.ChannelID, fmt.Sprintf("Pick a number between 1 and %d", len(ps.tracks)))
		return
	}

	// Find the user's voice channel
	gs := evt.GS
	if gs == nil {
		return
	}
	vs := gs.GetVoiceState(msg.Author.ID)
	if vs == nil || vs.ChannelID == 0 {
		common.BotSession.ChannelMessageSend(msg.ChannelID, "You're not in a voice channel")
		return
	}

	track := ps.tracks[n-1]
	queued := RequestPlay(guildID, vs.ChannelID, msg.ChannelID, track.URL, track.Title, track.Uploader)

	display := (&PlayRequest{URL: track.URL, Title: track.Title, Uploader: track.Uploader}).DisplayName()
	if queued {
		common.BotSession.ChannelMessageSend(msg.ChannelID, "Queued: "+display)
	} else {
		common.BotSession.ChannelMessageSend(msg.ChannelID, "Playing: "+display)
	}
}
