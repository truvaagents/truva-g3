BEGIN;

CREATE TABLE IF NOT EXISTS portability_workflow_executions (
    namespace     TEXT        NOT NULL,
    execution_id  TEXT        NOT NULL,
    workflow_id   TEXT        NOT NULL,
    payload       JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, execution_id)
);

CREATE INDEX IF NOT EXISTS portability_workflow_by_workflow
    ON portability_workflow_executions (namespace, workflow_id, created_at DESC);

CREATE TABLE IF NOT EXISTS portability_schedules (
    namespace    TEXT        NOT NULL,
    schedule_id  TEXT        NOT NULL,
    run_at       TIMESTAMPTZ NOT NULL,
    enabled      BOOLEAN     NOT NULL,
    payload      JSONB       NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, schedule_id)
);

CREATE INDEX IF NOT EXISTS portability_schedules_due
    ON portability_schedules (namespace, enabled, run_at);

CREATE TABLE IF NOT EXISTS portability_tasks (
    namespace   TEXT        NOT NULL,
    task_id     TEXT        NOT NULL,
    status      TEXT        NOT NULL,
    payload     JSONB       NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace, task_id)
);

CREATE INDEX IF NOT EXISTS portability_tasks_status
    ON portability_tasks (namespace, status, created_at DESC);

COMMIT;
