-- Playback protocol v3 release/validation queries.
-- Bind :since to the beginning of the validation window.

-- Route outcomes and failures by client/device.
SELECT event, COALESCE(failure_classification, '') AS failure,
       COALESCE(client_name, '') AS client, COALESCE(client_model, '') AS model,
       COUNT(*) AS events
FROM playback_route_events
WHERE received_at >= :since
GROUP BY 1, 2, 3, 4
ORDER BY events DESC;

-- First-frame success rate and latency. Diagnostics are allowlisted at ingest.
SELECT
    COUNT(*) FILTER (WHERE event = 'first_frame') AS first_frames,
    COUNT(*) FILTER (WHERE event = 'plan_selected') AS selected_plans,
    ROUND(100.0 * COUNT(*) FILTER (WHERE event = 'first_frame') /
          NULLIF(COUNT(*) FILTER (WHERE event = 'plan_selected'), 0), 2) AS first_frame_pct,
    percentile_cont(0.5) WITHIN GROUP
        (ORDER BY CASE WHEN diagnostics->>'first_frame_ms' ~ '^[0-9]+([.][0-9]+)?$'
                       THEN (diagnostics->>'first_frame_ms')::double precision END)
        FILTER (WHERE event = 'first_frame') AS first_frame_p50_ms,
    percentile_cont(0.95) WITHIN GROUP
        (ORDER BY CASE WHEN diagnostics->>'first_frame_ms' ~ '^[0-9]+([.][0-9]+)?$'
                       THEN (diagnostics->>'first_frame_ms')::double precision END)
        FILTER (WHERE event = 'first_frame') AS first_frame_p95_ms
FROM playback_route_events
WHERE received_at >= :since;

-- Replans, terminals, passthrough/PCM recovery and repeated recipe failures.
SELECT
    COALESCE(failure_classification, fallback_reason, event) AS classification,
    diagnostics->>'pcm_recovery' AS pcm_recovery,
    diagnostics->>'retry_outcome' AS retry_outcome,
    COUNT(*) AS occurrences,
    COUNT(DISTINCT playback_attempt_id) AS attempts
FROM playback_route_events
WHERE received_at >= :since
  AND event IN ('plan_invalidated', 'plan_failed', 'terminal')
GROUP BY 1, 2, 3
ORDER BY occurrences DESC;

-- Current recipes and declared degradation transformations.
SELECT
    current_plan->>'delivery' AS delivery,
    current_plan->'effective_recipe'->>'dynamic_range' AS dynamic_range,
    transformation->>'name' AS transformation,
    COUNT(*) AS attempts
FROM playback_v3_attempts
LEFT JOIN LATERAL jsonb_array_elements(current_plan->'transformations') transformation ON TRUE
WHERE created_at >= :since
GROUP BY 1, 2, 3
ORDER BY attempts DESC;
