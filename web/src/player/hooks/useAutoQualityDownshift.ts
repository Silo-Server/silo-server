import {
  BufferingQualityController,
  type BufferingAnswer,
  type BufferingEvent,
  type BufferingMode,
  type BufferingPlan,
  type BufferingReplan,
  type BufferingSummary,
} from "../buffering-quality";
import { useEffect, useRef, useState, type RefObject } from "react";

interface Options {
  videoRef: RefObject<HTMLVideoElement | null>;
  enabled: boolean;
  sessionId: string;
  plan: BufferingPlan;
  planRevision: number;
  mode: BufferingMode;
  obscured: boolean;
  transportTrouble?: () => boolean;
  onReplan: (
    request: BufferingReplan,
    onAdopt: (planId?: string, planRevision?: number) => void,
  ) => void;
  onNotice: (key: "lowered" | "cant_lower") => void;
  onSummary?: (plan: BufferingPlan, summary: BufferingSummary) => void;
}

export function useAutoQualityDownshift(options: Options) {
  const latest = useRef(options);
  useEffect(() => {
    latest.current = options;
  });
  const controller = useRef<BufferingQualityController | null>(null);
  const requestedPlan = useRef<number | undefined>(undefined);
  const adoptedPlan = useRef<number | undefined>(undefined);
  const [pill, setPill] = useState<{
    planRevision: number;
    kind: "prompt" | "undo";
    remainingMs: number;
  } | null>(null);
  const refresh = () => {
    const c = controller.current;
    setPill(
      c?.pill
        ? { planRevision: latest.current.planRevision, kind: c.pill, remainingMs: c.remainingMs }
        : null,
    );
  };
  useEffect(() => {
    if (!options.enabled) return;
    const c = new BufferingQualityController(
      latest.current.mode,
      (output) => {
        if (output.type === "replan") {
          requestedPlan.current = undefined;
          latest.current.onReplan(output.body, (_id, revision) => {
            if (controller.current === c) requestedPlan.current = revision;
          });
        }
        if (output.type === "notice") latest.current.onNotice(output.key);
      },
      Date.now(),
      (plan, summary) => latest.current.onSummary?.(plan, summary),
    );
    controller.current = c;
    adoptedPlan.current = undefined;
    return () => {
      c.stop(Date.now());
      controller.current = null;
    };
  }, [options.sessionId, options.enabled]);
  useEffect(() => {
    const c = controller.current;
    if (!c) return;
    c.mode = options.mode;
    c.setObscured(options.obscured, Date.now());
    const isNewPlan = adoptedPlan.current !== options.planRevision;
    adoptedPlan.current = options.planRevision;
    c.event(
      {
        type: "plan_adopted",
        plan: { ...options.plan, adoption_id: String(options.planRevision) },
        controllerInitiated: requestedPlan.current === options.planRevision,
      },
      Date.now(),
    );
    if (isNewPlan) requestedPlan.current = undefined;
    const video = options.videoRef.current;
    if (
      isNewPlan &&
      video &&
      !video.paused &&
      video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA
    )
      c.event({ type: "playing" }, Date.now());
  }, [
    options.plan,
    options.planRevision,
    options.mode,
    options.obscured,
    options.enabled,
    options.sessionId,
    options.videoRef,
  ]);
  useEffect(() => {
    const video = options.videoRef.current;
    const c = controller.current;
    if (!video || !c) return;
    let trouble = false;
    let stalledAt: number | undefined;
    const syncTrouble = () => {
      const next = latest.current.transportTrouble?.() ?? false;
      if (next !== trouble)
        c.event({ type: next ? "transport_error_start" : "transport_error_end" }, Date.now());
      trouble = next;
    };
    const send = (type: BufferingEvent["type"], cause?: BufferingEvent["cause"]) => {
      syncTrouble();
      c.event({ type, cause }, Date.now());
      refresh();
    };
    const playing = () => {
      stalledAt = undefined;
      send("playing");
    };
    const waiting = () => {
      stalledAt = video.currentTime;
      send("waiting", video.seeking ? "seek" : video.paused ? "pause" : undefined);
    };
    const pause = () => send("pause");
    const play = () => send("play");
    const seek = () => {
      stalledAt = undefined;
      send("seek");
    };
    const reload = () => {
      stalledAt = undefined;
      send("reload");
    };
    const timeupdate = () => {
      if (stalledAt !== undefined && video.currentTime > stalledAt) playing();
    };
    const events = {
      playing,
      play,
      waiting,
      pause,
      seeking: seek,
      emptied: reload,
      ended: pause,
      timeupdate,
    };
    for (const [name, handler] of Object.entries(events)) video.addEventListener(name, handler);
    if (!video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) playing();
    const timer = setInterval(() => {
      syncTrouble();
      c.tick(Date.now());
      refresh();
    }, 100);
    return () => {
      clearInterval(timer);
      for (const [name, handler] of Object.entries(events))
        video.removeEventListener(name, handler);
    };
  }, [options.videoRef, options.enabled, options.sessionId]);
  const answer = (type: BufferingAnswer | "quality_changed") => {
    controller.current?.event({ type }, Date.now());
    refresh();
  };
  return {
    pill:
      options.enabled && !options.obscured && pill?.planRevision === options.planRevision
        ? pill
        : null,
    answer,
  };
}
