package nodepool

// proxyCapabilityPayload is what a proxy reports now: what its ffmpeg can do,
// and no hardware inventory at all.
const proxyCapabilityPayload = `{"resolved":"none","source":"local",` +
	`"transformations":[{"name":"audio_to_aac","recipe_version":"2"}],` +
	`"capability_hash":"sha256:proxy-new"}`
