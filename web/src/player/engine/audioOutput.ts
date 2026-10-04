import workletUrl from "./audio.worklet.ts?worker&url";
import { outputChannelCount } from "./audioMix";
import type { PageToWorkletMessage, WorkletToPageMessage } from "./protocol";

const RESUME_TIMEOUT_MS = 1_500;

/**
 * The page's side of the engine's audio: an AudioContext opened at the
 * track's own rate (the browser resamples to the device), the worklet that
 * plays the decoded PCM, and a gain stage for volume.
 */
export class EngineAudioOutput {
  readonly channels: number;

  private constructor(
    readonly context: AudioContext,
    private readonly node: AudioWorkletNode,
    private readonly gain: GainNode,
    channels: number,
  ) {
    this.channels = channels;
  }

  static async create(
    sampleRate: number,
    sourceChannels: number,
    onMessage: (message: WorkletToPageMessage) => void,
  ): Promise<EngineAudioOutput> {
    const context = new AudioContext({ sampleRate, latencyHint: "playback" });
    try {
      await context.audioWorklet.addModule(workletUrl);
      const channels = outputChannelCount(sourceChannels, context.destination.maxChannelCount);
      if (channels > 2) {
        context.destination.channelCount = channels;
        context.destination.channelCountMode = "explicit";
        context.destination.channelInterpretation = channels === 6 ? "speakers" : "discrete";
      }
      const node = new AudioWorkletNode(context, "silo-engine-audio", {
        numberOfInputs: 0,
        numberOfOutputs: 1,
        outputChannelCount: [channels],
        processorOptions: { channels },
      });
      node.port.onmessage = (event: MessageEvent<WorkletToPageMessage>) => onMessage(event.data);
      const gain = context.createGain();
      node.connect(gain).connect(context.destination);
      return new EngineAudioOutput(context, node, gain, channels);
    } catch (error) {
      void context.close().catch(() => {});
      throw error;
    }
  }

  /** Hands the decode worker a direct line to the worklet. */
  connect(): MessagePort {
    const channel = new MessageChannel();
    this.send({ type: "connect", port: channel.port1 }, [channel.port1]);
    return channel.port2;
  }

  setGeneration(generation: number): void {
    this.send({ type: "generation", generation });
  }

  setPlaying(playing: boolean): void {
    this.send({ type: "playing", playing });
  }

  setRate(rate: number): void {
    this.send({ type: "rate", rate });
  }

  setVolume(volume: number, muted: boolean): void {
    this.gain.gain.value = muted ? 0 : Math.max(0, Math.min(1, volume));
  }

  /**
   * Starts the context. Browsers refuse without a recent user gesture; false
   * then means autoplay was blocked, the same as a rejected media element play.
   */
  async resume(): Promise<boolean> {
    if (this.context.state === "running") return true;
    await Promise.race([
      this.context.resume().catch(() => {}),
      new Promise((resolve) => setTimeout(resolve, RESUME_TIMEOUT_MS)),
    ]);
    return (this.context.state as AudioContextState) === "running";
  }

  /** Context time of the sample reaching the speakers now. */
  audibleContextTime(): number {
    const stamp = this.context.getOutputTimestamp?.();
    if (stamp && stamp.contextTime && stamp.performanceTime) {
      return stamp.contextTime + (performance.now() - stamp.performanceTime) / 1000;
    }
    const latency = this.context.outputLatency || this.context.baseLatency || 0;
    return this.context.currentTime - latency;
  }

  close(): void {
    this.node.port.onmessage = null;
    this.node.disconnect();
    this.gain.disconnect();
    void this.context.close().catch(() => {});
  }

  private send(message: PageToWorkletMessage, transfer: Transferable[] = []): void {
    this.node.port.postMessage(message, transfer);
  }
}
