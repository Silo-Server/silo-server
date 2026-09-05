// Unmodified excerpts from jellyfin/jellyfin-web v10.11.8, GPL-2.0.
// Source paths: src/components/playback/playbackmanager.js and src/plugins/htmlVideoPlayer/plugin.js.
    seekable() {
        const mediaElement = this.#mediaElement;
        if (mediaElement) {
            const seekable = mediaElement.seekable;
            if (seekable?.length) {
                let start = seekable.start(0);
                let end = seekable.end(0);

                if (!isValidDuration(start)) {
                    start = 0;
                }
                if (!isValidDuration(end)) {
                    end = 0;
                }

                return (end - start) > 0;
            }

            return false;
        }
    }
