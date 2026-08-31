SELECT
    namespace,
    task_id,
    status,
    payload->>'schedule_id' AS schedule_id,
    payload->>'target_agent' AS target_agent,
    payload->'result'->>'target_agent' AS result_target,
    created_at,
    updated_at
FROM portability_tasks
ORDER BY created_at DESC
LIMIT 20;
