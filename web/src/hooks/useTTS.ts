import { useCallback, useEffect, useRef, useState } from "react";

export type TTSState = "idle" | "speaking" | "paused";

export interface TTSOptions {
  rate?: number;
  pitch?: number;
  volume?: number;
  voiceURI?: string;
  lang?: string;
}

export function useTTS() {
  const [state, setState] = useState<TTSState>("idle");
  const [voices, setVoices] = useState<SpeechSynthesisVoice[]>([]);
  const currentUtter = useRef<SpeechSynthesisUtterance | null>(null);

  useEffect(() => {
    if (typeof window === "undefined" || !("speechSynthesis" in window)) return;
    const update = () => setVoices(window.speechSynthesis.getVoices());
    update();
    window.speechSynthesis.addEventListener("voiceschanged", update);
    return () => window.speechSynthesis.removeEventListener("voiceschanged", update);
  }, []);

  const stop = useCallback(() => {
    if (!("speechSynthesis" in window)) return;
    window.speechSynthesis.cancel();
    currentUtter.current = null;
    setState("idle");
  }, []);

  const pause = useCallback(() => {
    if (!("speechSynthesis" in window)) return;
    window.speechSynthesis.pause();
    setState("paused");
  }, []);

  const resume = useCallback(() => {
    if (!("speechSynthesis" in window)) return;
    window.speechSynthesis.resume();
    setState("speaking");
  }, []);

  const speak = useCallback(
    (text: string, opts: TTSOptions = {}) => {
      if (!("speechSynthesis" in window)) return;
      window.speechSynthesis.cancel();
      const cleaned = text.replace(/\s+/g, " ").trim();
      if (!cleaned) return;

      const sentences = cleaned.match(/[^.!?]+[.!?]+|\S[^.!?]*$/g) ?? [cleaned];
      const queue: string[] = [];
      for (const sentence of sentences) {
        let chunk = sentence.trim();
        while (chunk.length > 200) {
          const cut = chunk.lastIndexOf(" ", 200);
          queue.push(chunk.slice(0, cut > 0 ? cut : 200));
          chunk = chunk.slice(cut > 0 ? cut + 1 : 200);
        }
        if (chunk) queue.push(chunk);
      }

      const speakNext = () => {
        const next = queue.shift();
        if (!next) {
          setState("idle");
          currentUtter.current = null;
          installMediaSession(null, { pause, resume, stop });
          return;
        }
        const utterance = new SpeechSynthesisUtterance(next);
        utterance.rate = opts.rate ?? 1;
        utterance.pitch = opts.pitch ?? 1;
        utterance.volume = opts.volume ?? 1;
        if (opts.lang) utterance.lang = opts.lang;
        if (opts.voiceURI) {
          const voice = window.speechSynthesis
            .getVoices()
            .find((candidate) => candidate.voiceURI === opts.voiceURI);
          if (voice) utterance.voice = voice;
        }
        utterance.onend = speakNext;
        utterance.onerror = speakNext;
        currentUtter.current = utterance;
        window.speechSynthesis.speak(utterance);
      };

      setState("speaking");
      installMediaSession({ title: "Read aloud" }, { pause, resume, stop });
      speakNext();
    },
    [pause, resume, stop],
  );

  useEffect(() => {
    return () => {
      if ("speechSynthesis" in window) window.speechSynthesis.cancel();
    };
  }, []);

  return { state, voices, speak, pause, resume, stop };
}

function installMediaSession(
  meta: { title?: string } | null,
  handlers: { pause: () => void; resume: () => void; stop: () => void },
) {
  if (typeof navigator === "undefined" || !("mediaSession" in navigator)) return;
  if (meta) {
    navigator.mediaSession.metadata = new window.MediaMetadata({
      title: meta.title ?? "Read aloud",
    });
    navigator.mediaSession.playbackState = "playing";
    navigator.mediaSession.setActionHandler("play", handlers.resume);
    navigator.mediaSession.setActionHandler("pause", handlers.pause);
    navigator.mediaSession.setActionHandler("stop", handlers.stop);
  } else {
    navigator.mediaSession.metadata = null;
    navigator.mediaSession.playbackState = "none";
    navigator.mediaSession.setActionHandler("play", null);
    navigator.mediaSession.setActionHandler("pause", null);
    navigator.mediaSession.setActionHandler("stop", null);
  }
}
