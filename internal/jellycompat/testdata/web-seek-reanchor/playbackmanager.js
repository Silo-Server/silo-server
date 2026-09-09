// Unmodified excerpts from jellyfin/jellyfin-web v10.11.8, GPL-2.0.
// Source paths: src/components/playback/playbackmanager.js and src/plugins/htmlVideoPlayer/plugin.js.
async function getPlaybackInfo(player, apiClient, item, deviceProfile, mediaSourceId, liveStreamId, options) {
    if (!itemHelper.isLocalItem(item) && item.MediaType === 'Audio' && !player.useServerPlaybackInfoForAudio) {
        return {
            MediaSources: [
                {
                    StreamUrl: getAudioStreamUrlFromDeviceProfile(item, deviceProfile, options.maxBitrate, apiClient, options.startPosition),
                    Id: item.Id,
                    MediaStreams: [],
                    RunTimeTicks: item.RunTimeTicks
                }]
        };
    }

    if (item.PresetMediaSource) {
        return {
            MediaSources: [item.PresetMediaSource]
        };
    }

    const itemId = item.Id;

    const query = {
        UserId: apiClient.getCurrentUserId(),
        StartTimeTicks: options.startPosition || 0
    };

    const api = toApi(apiClient);
    const mediaInfoApi = getMediaInfoApi(api);

    if (options.isPlayback) {
        query.IsPlayback = true;
        query.AutoOpenLiveStream = true;
    } else {
        query.IsPlayback = false;
        query.AutoOpenLiveStream = false;
    }

    if (options.audioStreamIndex != null) {
        query.AudioStreamIndex = options.audioStreamIndex;
    }
    if (options.subtitleStreamIndex != null) {
        query.SubtitleStreamIndex = options.subtitleStreamIndex;
    }
    if (options.secondarySubtitleStreamIndex != null) {
        query.SecondarySubtitleStreamIndex = options.secondarySubtitleStreamIndex;
    }
    if (options.enableDirectPlay != null) {
        query.EnableDirectPlay = options.enableDirectPlay;
    }
    if (options.enableDirectStream != null) {
        query.EnableDirectStream = options.enableDirectStream;
    }
    if (options.allowVideoStreamCopy != null) {
        query.AllowVideoStreamCopy = options.allowVideoStreamCopy;
    }
    if (options.allowAudioStreamCopy != null) {
        query.AllowAudioStreamCopy = options.allowAudioStreamCopy;
    }
    if (mediaSourceId) {
        query.MediaSourceId = mediaSourceId;
    }
    if (liveStreamId) {
        query.LiveStreamId = liveStreamId;
    }
    if (options.maxBitrate) {
        query.MaxStreamingBitrate = options.maxBitrate;
    }
    if (player.enableMediaProbe && !player.enableMediaProbe(item)) {
        query.EnableMediaProbe = false;
    }

    // lastly, enforce player overrides for special situations
    if (query.EnableDirectStream !== false
        && player.supportsPlayMethod && !player.supportsPlayMethod('DirectStream', item)
    ) {
        query.EnableDirectStream = false;
    }

    if (player.getDirectPlayProtocols) {
        query.DirectPlayProtocols = player.getDirectPlayProtocols();
    }

    query.AlwaysBurnInSubtitleWhenTranscoding = appSettings.alwaysBurnInSubtitleWhenTranscoding();

    query.DeviceProfile = deviceProfile;

    const res = await mediaInfoApi.getPostedPlaybackInfo({ itemId: itemId, playbackInfoDto: query });
    return res.data;
}
        function changeStream(player, ticks, params) {
            if (canPlayerSeek(player) && params == null) {
                player.currentTime(parseInt(ticks / 10000, 10));
                return;
            }

            params = params || {};

            const liveStreamId = getPlayerData(player).streamInfo.liveStreamId;
            const lastMediaInfoQuery = getPlayerData(player).streamInfo.lastMediaInfoQuery;

            const playSessionId = self.playSessionId(player);

            const currentItem = self.currentItem(player);

            player.getDeviceProfile(currentItem, {
                isRetry: params.EnableDirectPlay === false
            }).then(function (deviceProfile) {
                const audioStreamIndex = params.AudioStreamIndex == null ? getPlayerData(player).audioStreamIndex : params.AudioStreamIndex;
                const subtitleStreamIndex = params.SubtitleStreamIndex == null ? getPlayerData(player).subtitleStreamIndex : params.SubtitleStreamIndex;
                const secondarySubtitleStreamIndex = params.SecondarySubtitleStreamIndex == null ? getPlayerData(player).secondarySubtitleStreamIndex : params.SecondarySubtitleStreamIndex;

                let currentMediaSource = self.currentMediaSource(player);
                const apiClient = ServerConnections.getApiClient(currentItem.ServerId);

                if (ticks) {
                    ticks = parseInt(ticks, 10);
                }

                const maxBitrate = params.MaxStreamingBitrate || self.getMaxStreamingBitrate(player);

                const currentPlayOptions = currentItem.playOptions || getDefaultPlayOptions();

                const options = {
                    maxBitrate,
                    startPosition: ticks,
                    isPlayback: true,
                    audioStreamIndex,
                    subtitleStreamIndex,
                    enableDirectPlay: params.EnableDirectPlay,
                    enableDirectStream: params.EnableDirectStream,
                    allowVideoStreamCopy: params.AllowVideoStreamCopy,
                    allowAudioStreamCopy: params.AllowAudioStreamCopy
                };

                getPlaybackInfo(player, apiClient, currentItem, deviceProfile, currentMediaSource.Id, liveStreamId, options).then(function (result) {
                    if (validatePlaybackInfoResult(self, result)) {
                        currentMediaSource = result.MediaSources[0];

                        const streamInfo = createStreamInfo(apiClient, currentItem.MediaType, currentItem, currentMediaSource, ticks, player);
                        streamInfo.fullscreen = currentPlayOptions.fullscreen;
                        streamInfo.lastMediaInfoQuery = lastMediaInfoQuery;
                        streamInfo.resetSubtitleOffset = false;

                        if (!streamInfo.url) {
                            cancelPlayback();
                            showPlaybackInfoErrorMessage(self, `PlaybackError.${MediaError.NO_MEDIA_ERROR}`);
                            return;
                        }

                        getPlayerData(player).subtitleStreamIndex = subtitleStreamIndex;
                        getPlayerData(player).secondarySubtitleStreamIndex = secondarySubtitleStreamIndex;
                        getPlayerData(player).audioStreamIndex = audioStreamIndex;
                        getPlayerData(player).maxStreamingBitrate = maxBitrate;

                        changeStreamToUrl(apiClient, player, playSessionId, streamInfo);
                    }
                });
            });
        }
