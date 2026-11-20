package jellycompat

// compatTestFFmpegProgress is the -progress stream a real video encode writes to
// stdout while it muxes. A fake FFmpeg that stands for a playable encode must
// report decoded frames: the server holds a start until the generation's frame
// counter moves, so a silent fake would look exactly like an encoder whose
// decoder produced no picture at all.
const compatTestFFmpegProgress = "printf 'frame=48\\nout_time_us=4000000\\nprogress=continue\\n';\n"
