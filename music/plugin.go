package music

import (
	"github.com/botlabs-gg/yagpdb/v2/bot/eventsystem"
	"github.com/botlabs-gg/yagpdb/v2/common"
)

type Plugin struct{}

var logger = common.GetFixedPrefixLogger("music")

func (p *Plugin) PluginInfo() *common.PluginInfo {
	return &common.PluginInfo{
		Name:     "Music",
		SysName:  "music",
		Category: common.PluginCategoryMisc,
	}
}

func (p *Plugin) BotInit() {
	eventsystem.AddHandlerAsyncLastLegacy(p, handleMessageCreate, eventsystem.EventMessageCreate)
}

func RegisterPlugin() {
	p := &Plugin{}
	common.RegisterPlugin(p)
}
