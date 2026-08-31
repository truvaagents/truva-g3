SELECT
    namespace,
    execution_id,
    workflow_id,
    payload->>'status' AS status,
    payload->'outputs'->>'summary' AS summary,
    created_at,
    updated_at
FROM portability_workflow_executions
ORDER BY created_at DESC
LIMIT 20;
