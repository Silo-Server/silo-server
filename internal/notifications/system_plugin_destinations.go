package notifications

// SetPluginChannelHost wires the plugin runtime into plugin destinations.
// Until it is called they list no kinds, queue nothing and send nothing.
func (s *System) SetPluginChannelHost(host PluginChannelHost) {
	if s == nil || s.pluginDestinationSender == nil || host == nil {
		return
	}
	s.pluginDestinationSender.hostBox.Store(&pluginChannelHostBox{host: host})
}

// wirePluginDestinationSender gives the plugin destination sender the
// system's rendering and operational hooks once the System exists.
func (s *System) wirePluginDestinationSender() {
	if s.pluginDestinationSender == nil {
		return
	}
	s.pluginDestinationSender.operational = s.DispatchOperational
	s.pluginDestinationSender.posterURL = s.discordPosterURL
	s.pluginDestinationSender.linkBase = s.emailLinkBase
	s.pluginDestinationSender.payload = s.PayloadForRow
}
