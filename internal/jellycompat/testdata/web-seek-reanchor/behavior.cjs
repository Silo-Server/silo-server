const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const [managerPath, playerPath] = process.argv.slice(2);
const HtmlVideoPlayer = vm.runInThisContext(`(class {
    #mediaElement;
    constructor(element) { this.#mediaElement = element; }
    ${fs.readFileSync(playerPath, 'utf8')}
})`);
vm.runInThisContext(fs.readFileSync(managerPath, 'utf8'));

global.isValidDuration = Number.isFinite;
global.itemHelper = { isLocalItem: () => false };
global.toApi = value => value;
global.appSettings = { alwaysBurnInSubtitleWhenTranscoding: () => false };
const requests = [];
global.getMediaInfoApi = () => ({ getPostedPlaybackInfo: async request => {
    requests.push(request);
    return { data: {} };
} });
global.canPlayerSeek = () => true;
let generationStart = 0;
global.getPlayerData = () => ({ streamInfo: { playerStartPositionTicks: generationStart } });
global.validatePlaybackInfoResult = () => false;
const item = { Id: 'item', ServerId: 'server', MediaType: 'Video', playOptions: {} };
const api = { getCurrentUserId: () => 'viewer' };
global.ServerConnections = { getApiClient: () => api };
let flagged = true;
global.self = {
    currentMediaSource: () => ({ Id: 'source', SiloSeekReanchor: flagged }),
    currentItem: () => item,
    playSessionId: () => 'session',
    getMaxStreamingBitrate: () => 4000000
};

function playerFor(intervals) {
    const player = new HtmlVideoPlayer({ seekable: {
        length: intervals.length,
        start: i => intervals[i][0], end: i => intervals[i][1]
    } });
    player.currentTime = value => { player.localSeek = value; };
    player.getDeviceProfile = async () => ({});
    return player;
}

(async () => {
    const ranges = playerFor([[0, 10], [30, 40]]);
    for (const value of [0, 5000, 10000, 30000, 40000]) assert.equal(ranges.canSeekTo(value), true);
    for (const value of [-1, NaN, Infinity, '5000', 10001, 20000, 40001]) assert.equal(ranges.canSeekTo(value), false);
    assert.equal(playerFor([]).canSeekTo(0), false);
    assert.equal(new HtmlVideoPlayer(null).canSeekTo(0), false);

    for (const test of [
        { name: 'produced range', target: 5000, local: true },
        { name: 'second produced range', target: 35000, local: true },
        { name: 'unproduced gap', target: 20000, local: false },
        { name: 'beyond produced tail', target: 60000, local: false },
        { name: 'nonzero anchor before range', target: 5000, intervals: [[30, 40]], local: false },
        { name: 'nonzero anchor inside range', target: 35000, intervals: [[30, 40]], local: true },
        { name: 'backward into advertised gap prefix', target: 600000, start: 900000, intervals: [[0, 920]], local: false },
        { name: 'inside active generation', target: 910000, start: 900000, intervals: [[0, 920]], local: true },
        { name: 'unflagged source', target: 60000, flagged: false, local: true },
        { name: 'flagged player without method', target: 5000, missing: true, local: false },
        { name: 'unflagged player without method', target: 5000, missing: true, flagged: false, local: true },
        { name: 'track change still renegotiates', target: 5000, params: { AudioStreamIndex: 2 }, local: false }
    ]) {
        requests.length = 0;
        flagged = test.flagged !== false;
        generationStart = (test.start || 0) * 10000;
        const player = playerFor(test.intervals || [[0, 10], [30, 40]]);
        if (test.missing) player.canSeekTo = undefined;
        changeStream(player, test.target * 10000, test.params);
        await new Promise(setImmediate);
        assert.equal(player.localSeek, test.local ? test.target : undefined, test.name);
        assert.equal(requests.length, test.local ? 0 : 1, test.name);
        if (!test.local) {
            assert.equal(requests[0].playbackInfoDto.StartTimeTicks, test.target * 10000, test.name);
            assert.equal(requests[0].playbackInfoDto.SiloSeekReanchor, true, test.name);
        }
    }
    // Initial PlaybackInfo, not only the renegotiation path, opts in.
    requests.length = 0;
    await getPlaybackInfo({}, api, item, {}, null, null, { startPosition: 123456789 });
    assert.equal(requests[0].playbackInfoDto.SiloSeekReanchor, true);
    assert.equal(requests[0].playbackInfoDto.StartTimeTicks, 123456789);
})().catch(error => { console.error(error); process.exitCode = 1; });
