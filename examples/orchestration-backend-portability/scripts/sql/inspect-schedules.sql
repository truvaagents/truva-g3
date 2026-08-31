SELECT
    namespace,
    schedule_id,
    run_at,
    enabled,
    payload->>'target_agent' AS target_agent,
    created_at,
    updated_at
FROM portability_schedules
ORDER BY created_at DESC
LIMIT 20;
