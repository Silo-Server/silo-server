import { describe, expect, it } from "vitest";
import {
  ALL_PLAYBACK_COMMANDS,
  buildPlaybackRealtimeAck,
  buildPlaybackRealtimeHello,
  buildPlaybackRealtimeResult,
  parsePlaybackRealtimeMessage,
  parsePlaybackRealtimeCommand,
  readPlanInvalidatedPayload,
  SUPPORTED_PLAYBACK_COMMANDS,
  VIDEO_PLAYBACK_COMMANDS,
} from "./realtime-protocol";

describe("realtime protocol", () => {
  it("parses known command envelopes", () => {
    const command = parsePlaybackRealtimeCommand(
      JSON.stringify({
        type: "command",
        command_id: "cmd-1",
        session_id: "session-1",
        name: "server_restarting",
        payload: { message: "Restarting soon" },
      }),
    );

    expect(command).toEqual({
      type: "command",
      command_id: "cmd-1",
      session_id: "session-1",
      name: "server_restarting",
      reason: undefined,
      issued_by: undefined,
      deadline_ms: undefined,
      payload: { message: "Restarting soon" },
    });
  });

  it("parses a plan invalidation command and its payload", () => {
    const command = parsePlaybackRealtimeCommand(
      JSON.stringify({
        type: "command",
        command_id: "cmd-9",
        session_id: "session-1",
        name: "plan_invalidated",
        deadline_ms: 8_000,
        payload: { reason: "video_copy_unsafe", plan_id: "plan:0123456789abcdef" },
      }),
    );

    expect(command).toMatchObject({
      type: "command",
      command_id: "cmd-9",
      name: "plan_invalidated",
      deadline_ms: 8_000,
    });
    expect(readPlanInvalidatedPayload(command?.payload)).toEqual({
      reason: "video_copy_unsafe",
      plan_id: "plan:0123456789abcdef",
    });
  });

  it("rejects a plan invalidation payload missing the invalidated plan", () => {
    // Without the plan id the client cannot tell whether the plan it is playing
    // is the one that was invalidated, so acting on it is never correct.
    expect(readPlanInvalidatedPayload({ reason: "video_copy_unsafe" })).toBeNull();
    expect(readPlanInvalidatedPayload({ reason: "", plan_id: "plan:1" })).toBeNull();
    expect(readPlanInvalidatedPayload({ reason: "video_copy_unsafe", plan_id: 42 })).toBeNull();
    expect(readPlanInvalidatedPayload(undefined)).toBeNull();
  });

  // The audiobook surface shares this module and cannot replan off an
  // invalidated plan, so only the video command set announces it.
  it("announces plan invalidation only for the video surface", () => {
    expect(SUPPORTED_PLAYBACK_COMMANDS).not.toContain("plan_invalidated");
    expect(VIDEO_PLAYBACK_COMMANDS).toContain("plan_invalidated");
    expect(
      buildPlaybackRealtimeHello("session-1", VIDEO_PLAYBACK_COMMANDS).capabilities.commands,
    ).toContain("plan_invalidated");
  });

  it("rejects unknown commands", () => {
    const command = parsePlaybackRealtimeCommand(
      JSON.stringify({
        type: "command",
        command_id: "cmd-1",
        session_id: "session-1",
        name: "launch_missiles",
      }),
    );

    expect(command).toBeNull();
  });

  it("parses chapter thumbnail events", () => {
    const event = parsePlaybackRealtimeMessage(
      JSON.stringify({
        type: "event",
        session_id: "session-1",
        name: "chapter_thumbnail_ready",
        payload: {
          session_id: "session-1",
          file_id: 42,
          chapter_index: 3,
          thumbnail_url: "https://example.com/thumb.jpg",
          thumbnail_thumbhash: "thumbhash",
        },
      }),
    );

    expect(event).toEqual({
      type: "event",
      session_id: "session-1",
      name: "chapter_thumbnail_ready",
      payload: {
        session_id: "session-1",
        file_id: 42,
        chapter_index: 3,
        thumbnail_url: "https://example.com/thumb.jpg",
        thumbnail_thumbhash: "thumbhash",
      },
    });
  });

  it("parses marker update events", () => {
    const event = parsePlaybackRealtimeMessage(
      JSON.stringify({
        type: "event",
        session_id: "session-1",
        name: "markers_updated",
        payload: {
          session_id: "session-1",
          file_id: 42,
          intro: { start: 12, end: 75 },
          credits: null,
        },
      }),
    );

    expect(event).toEqual({
      type: "event",
      session_id: "session-1",
      name: "markers_updated",
      payload: {
        session_id: "session-1",
        file_id: 42,
        intro: { start: 12, end: 75 },
        credits: null,
      },
    });
  });

  it("parses subtitle ready events", () => {
    const event = parsePlaybackRealtimeMessage(
      JSON.stringify({
        type: "event",
        session_id: "session-1",
        name: "subtitle_ready",
        payload: {
          session_id: "session-1",
          file_id: 42,
          subtitle_id: 7,
          language: "es",
          label: "English → Spanish (AI)",
        },
      }),
    );

    expect(event).toEqual({
      type: "event",
      session_id: "session-1",
      name: "subtitle_ready",
      payload: {
        session_id: "session-1",
        file_id: 42,
        subtitle_id: 7,
        language: "es",
        label: "English → Spanish (AI)",
      },
    });
  });

  it("rejects subtitle ready events missing the subtitle id", () => {
    const event = parsePlaybackRealtimeMessage(
      JSON.stringify({
        type: "event",
        session_id: "session-1",
        name: "subtitle_ready",
        payload: { session_id: "session-1", file_id: 42, language: "es" },
      }),
    );

    expect(event).toBeNull();
  });

  it("parses subtitle translation started/completed/failed events", () => {
    const startedPayload = {
      session_id: "s1",
      file_id: 42,
      job_id: 99,
      track_key: "ai-99",
      language: "es",
      total_cues: 120,
    };
    expect(
      parsePlaybackRealtimeMessage(
        JSON.stringify({
          type: "event",
          session_id: "s1",
          name: "subtitle_translation_started",
          payload: startedPayload,
        }),
      ),
    ).toEqual({
      type: "event",
      session_id: "s1",
      name: "subtitle_translation_started",
      payload: startedPayload,
    });

    const completedPayload = {
      session_id: "s1",
      file_id: 42,
      job_id: 99,
      track_key: "ai-99",
      subtitle_id: 7,
      language: "es",
    };
    expect(
      parsePlaybackRealtimeMessage(
        JSON.stringify({
          type: "event",
          session_id: "s1",
          name: "subtitle_translation_completed",
          payload: completedPayload,
        }),
      ),
    ).toEqual({
      type: "event",
      session_id: "s1",
      name: "subtitle_translation_completed",
      payload: completedPayload,
    });

    const failedPayload = { session_id: "s1", file_id: 42, job_id: 99, track_key: "ai-99" };
    expect(
      parsePlaybackRealtimeMessage(
        JSON.stringify({
          type: "event",
          session_id: "s1",
          name: "subtitle_translation_failed",
          payload: failedPayload,
        }),
      ),
    ).toEqual({
      type: "event",
      session_id: "s1",
      name: "subtitle_translation_failed",
      payload: failedPayload,
    });
  });

  it("parses translation cues and rejects malformed ones", () => {
    const cuesPayload = {
      session_id: "s1",
      file_id: 42,
      job_id: 99,
      track_key: "ai-99",
      cues: [{ start: 1, end: 2, text: "hola" }],
      done: 1,
      total: 120,
    };
    expect(
      parsePlaybackRealtimeMessage(
        JSON.stringify({
          type: "event",
          session_id: "s1",
          name: "subtitle_translation_cues",
          payload: cuesPayload,
        }),
      ),
    ).toEqual({
      type: "event",
      session_id: "s1",
      name: "subtitle_translation_cues",
      payload: cuesPayload,
    });

    // A cue missing `text` must fail the guard (every-cue validation).
    const bad = parsePlaybackRealtimeMessage(
      JSON.stringify({
        type: "event",
        session_id: "s1",
        name: "subtitle_translation_cues",
        payload: {
          session_id: "s1",
          file_id: 42,
          job_id: 99,
          track_key: "ai-99",
          cues: [{ start: 1, end: 2 }],
          done: 1,
          total: 120,
        },
      }),
    );
    expect(bad).toBeNull();

    // The optional `label` on a started event, when present, must be a string.
    const badLabel = parsePlaybackRealtimeMessage(
      JSON.stringify({
        type: "event",
        session_id: "s1",
        name: "subtitle_translation_started",
        payload: {
          session_id: "s1",
          file_id: 42,
          job_id: 99,
          track_key: "ai-99",
          language: "es",
          total_cues: 1,
          label: 5,
        },
      }),
    );
    expect(badLabel).toBeNull();
  });

  it("builds hello, ack, and result envelopes", () => {
    expect(buildPlaybackRealtimeHello("session-1")).toEqual({
      type: "hello",
      session_id: "session-1",
      client: {
        name: "silo-web",
        version: "1",
      },
      capabilities: {
        commands: SUPPORTED_PLAYBACK_COMMANDS,
      },
    });

    expect(buildPlaybackRealtimeAck("session-1", "cmd-1")).toEqual({
      type: "ack",
      command_id: "cmd-1",
      session_id: "session-1",
      status: "accepted",
    });

    expect(buildPlaybackRealtimeResult("session-1", "cmd-1", "rejected", "unsupported")).toEqual({
      type: "result",
      command_id: "cmd-1",
      session_id: "session-1",
      status: "rejected",
      error: "unsupported",
    });
  });

  it("keeps the supported command subset within the full command set", () => {
    expect(SUPPORTED_PLAYBACK_COMMANDS.every((name) => ALL_PLAYBACK_COMMANDS.includes(name))).toBe(
      true,
    );
  });
});
