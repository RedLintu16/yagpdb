package music

import (
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/botlabs-gg/yagpdb/v2/bot"
	"github.com/botlabs-gg/yagpdb/v2/common"
	"github.com/botlabs-gg/yagpdb/v2/lib/dca"
	"github.com/botlabs-gg/yagpdb/v2/lib/discordgo"
)

type PlayRequest struct {
	GuildID        int64
	ChannelID      int64
	CommandRanFrom int64
	URL            string
	Title          string
	Uploader       string
}

func (r *PlayRequest) DisplayName() string {
	if r.Title == "" {
		return "<" + r.URL + ">"
	}
	if r.Uploader != "" {
		return r.Uploader + " - " + r.Title
	}
	return r.Title
}

var (
	players   = make(map[int64]*Player)
	playersmu = sync.NewCond(&sync.Mutex{})

	Silence = []byte{0xF8, 0xFF, 0xFE}
)

// RequestPlay queues a URL to play, or starts a new player for the guild.
// Returns true if queued into an existing session.
func RequestPlay(guildID, channelID, channelRanFrom int64, url, title, uploader string) (queued bool) {
	req := &PlayRequest{
		GuildID:        guildID,
		ChannelID:      channelID,
		CommandRanFrom: channelRanFrom,
		URL:            url,
		Title:          title,
		Uploader:       uploader,
	}

	playersmu.L.Lock()
	if p, ok := players[guildID]; ok {
		p.queue = append(p.queue, req)
		queued = true
	} else {
		p = &Player{
			GuildID:   guildID,
			ChannelID: channelID,
			queue:     []*PlayRequest{req},
		}
		players[guildID] = p
		go p.Run()
	}
	playersmu.L.Unlock()
	playersmu.Broadcast()
	return
}

func getQueue(guildID int64) string {
	playersmu.L.Lock()
	defer playersmu.L.Unlock()

	p, ok := players[guildID]
	if !ok {
		return "Nothing is playing"
	}

	out := ""
	if p.currentReq != nil {
		out += "**Now playing:** " + p.currentReq.DisplayName() + "\n"
	}

	if len(p.queue) == 0 {
		out += "Queue is empty"
		return out
	}

	out += "**Up next:**\n"
	for i, req := range p.queue {
		out += fmt.Sprintf("%d. %s\n", i+1, req.DisplayName())
		if i >= 9 {
			out += fmt.Sprintf("...and %d more", len(p.queue)-10)
			break
		}
	}
	return out
}

func stopPlayer(guildID int64) string {
	playersmu.L.Lock()
	if p, ok := players[guildID]; ok {
		p.stop = true
		playersmu.L.Unlock()
		playersmu.Broadcast()
		return ""
	}
	playersmu.L.Unlock()
	return "Nothing is playing"
}

func skipPlayer(guildID int64) string {
	playersmu.L.Lock()
	defer playersmu.L.Unlock()
	p, ok := players[guildID]
	if !ok {
		return "Nothing is playing"
	}
	if !p.playing {
		return "Nothing is playing"
	}
	p.skip = true
	playersmu.Broadcast()
	return ""
}

func backPlayer(guildID int64) string {
	playersmu.L.Lock()
	defer playersmu.L.Unlock()
	p, ok := players[guildID]
	if !ok {
		return "Nothing is playing"
	}
	if len(p.history) == 0 {
		return "No previous tracks"
	}
	prev := p.history[len(p.history)-1]
	p.history = p.history[:len(p.history)-1]
	p.queue = append([]*PlayRequest{prev}, p.queue...)
	p.skip = true
	playersmu.Broadcast()
	return ""
}

// Player manages a voice connection for one guild.
type Player struct {
	GuildID   int64
	ChannelID int64

	// all fields below are protected by playersmu
	queue        []*PlayRequest
	history      []*PlayRequest // last N completed tracks
	currentReq   *PlayRequest
	timeLastPlay time.Time
	playing      bool
	stop         bool
	skip         bool

	vc *discordgo.VoiceConnection
}

func (p *Player) Run() {
	go p.checkIdleTooLong()

	for {
		p.waitForNext()
		if p.stop {
			playersmu.L.Unlock()
			return
		}

		req := p.queue[0]
		p.queue = p.queue[1:]
		p.currentReq = req

		changeChannel := p.ChannelID != req.ChannelID
		if changeChannel {
			p.ChannelID = req.ChannelID
		}

		p.playing = true
		p.timeLastPlay = time.Now()
		playersmu.L.Unlock()

		var err error
		p.vc, err = playURL(p, p.vc, bot.ShardManager.SessionForGuild(p.GuildID), req, changeChannel)
		if err != nil {
			logger.WithError(err).WithField("guild", p.GuildID).Error("Failed playing URL")
			if req.CommandRanFrom != 0 {
				common.BotSession.ChannelMessageSend(req.CommandRanFrom, "Failed playing: `"+err.Error()+"`")
			}
		}

		// Save completed track to history and reset skip flag
		playersmu.L.Lock()
		if !p.skip {
			// Only add to history if it finished naturally (not skipped back)
			p.history = append(p.history, req)
			if len(p.history) > 20 {
				p.history = p.history[1:]
			}
		}
		p.skip = false
		p.currentReq = nil
		p.timeLastPlay = time.Now() // reset idle timer from when song finished, not started
		playersmu.L.Unlock()
	}
}

// waitForNext blocks until the queue has an item. playersmu.L is locked on return.
func (p *Player) waitForNext() {
	playersmu.L.Lock()
	p.playing = false
	for {
		if p.stop {
			p.exit()
			return
		}
		if len(p.queue) > 0 {
			break
		}
		playersmu.Wait()
	}
}

func (p *Player) exit() {
	if p.vc != nil {
		p.vc.Disconnect()
	}
	p.queue = nil
	delete(players, p.GuildID)
}

func (p *Player) checkIdleTooLong() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		<-t.C
		playersmu.L.Lock()
		if p.stop {
			playersmu.L.Unlock()
			return
		}
		if !p.playing && len(p.queue) == 0 && time.Since(p.timeLastPlay) > time.Minute {
			p.stop = true
			playersmu.Broadcast()
			playersmu.L.Unlock()
			return
		}
		playersmu.L.Unlock()
	}
}

func playURL(p *Player, vc *discordgo.VoiceConnection, session *discordgo.Session, req *PlayRequest, changeChannel bool) (*discordgo.VoiceConnection, error) {
	// Stream audio from URL via yt-dlp
	ytdlp := exec.Command("yt-dlp",
		"-f", "bestaudio/best",
		"--no-playlist",
		"-o", "-",
		req.URL,
	)
	ytdlpOut, err := ytdlp.StdoutPipe()
	if err != nil {
		return vc, common.ErrWithCaller(err)
	}
	if err := ytdlp.Start(); err != nil {
		return vc, common.ErrWithCaller(err)
	}
	defer func() {
		if ytdlp.Process != nil {
			ytdlp.Process.Kill()
		}
		ytdlp.Wait()
	}()

	// Encode to Opus via ffmpeg (dca library handles this)
	opts := *dca.StdEncodeOptions
	opts.RawOutput = false
	encodeSession, err := dca.EncodeMem(ytdlpOut, &opts)
	if err != nil {
		return vc, common.ErrWithCaller(err)
	}
	defer encodeSession.Stop()

	// Join or switch voice channel
	if changeChannel || vc == nil || !vc.Ready {
		vc, err = session.GatewayManager.ChannelVoiceJoin(req.GuildID, req.ChannelID, false, true)
		if err != nil {
			if err == discordgo.ErrTimeoutWaitingForVoice {
				bot.ShardManager.SessionForGuild(req.GuildID).GatewayManager.ChannelVoiceLeave(req.GuildID)
			}
			return nil, common.ErrWithCaller(err)
		}
		<-vc.Connected
		vc.Speaking(true)
	}

	if err := sendSilence(vc, 3); err != nil {
		return vc, common.ErrWithCaller(err)
	}

	for {
		playersmu.L.Lock()
		if p.stop || p.skip {
			playersmu.L.Unlock()
			return vc, nil
		}
		playersmu.L.Unlock()

		frame, err := encodeSession.OpusFrame()
		if err != nil {
			if err != io.EOF {
				return vc, common.ErrWithCaller(err)
			}
			break
		}

		if err := sendAudio(vc, frame); err != nil {
			return vc, common.ErrWithCaller(err)
		}
	}

	if err := sendSilence(vc, 5); err != nil {
		return vc, common.ErrWithCaller(err)
	}

	return vc, nil
}

func sendSilence(vc *discordgo.VoiceConnection, n int) error {
	for i := 0; i < n; i++ {
		if err := sendAudio(vc, Silence); err != nil {
			return err
		}
	}
	return nil
}

func sendAudio(vc *discordgo.VoiceConnection, frame []byte) error {
	select {
	case vc.OpusSend <- frame:
	case <-time.After(time.Second):
		return discordgo.ErrTimeoutWaitingForVoice
	}
	return nil
}
