package main

import (
	"os"

	"github.com/truvaagents/truva-g3/core"
)

// JiraTool wraps the JIRA Cloud REST API v3 for issue management.
type JiraTool struct {
	*core.BaseTool
	client *JiraClient
}

// NewJiraTool creates and initializes the JIRA tool.
func NewJiraTool() *JiraTool {
	tool := &JiraTool{
		BaseTool: core.NewTool("jira-tool"),
		client: NewJiraClient(
			os.Getenv("JIRA_BASE_URL"),
			os.Getenv("JIRA_USER_EMAIL"),
			os.Getenv("JIRA_API_TOKEN"),
		),
	}

	tool.registerCapabilities()
	return tool
}

func (t *JiraTool) registerCapabilities() {
	// 5.1 get_issue
	t.RegisterCapability(core.Capability{
		Name: "get_issue",

		// Phase 1: Tool selection — WHAT + WHEN + RETURNS
		Description: "Gets a single JIRA issue by key. " +
			"Use when you know the issue key and need its current details. " +
			"Returns: summary, status, assignee, priority, labels, description, and dates.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetIssue,

		// Phase 2: Payload generation — exact field names, types, examples
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Example: "PROJ-123",
					Description: "Issue key or numeric ID"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "fields", Type: "string", Example: "summary,status,assignee",
					Description: "Comma-separated field names to return. Omit for all fields"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "key", Type: "string", Description: "Issue key (e.g. PROJ-123)"},
				{Name: "id", Type: "string", Description: "Issue numeric ID"},
				{Name: "browse_url", Type: "string", Description: "Human-facing browse link for the issue (e.g. https://your-domain.atlassian.net/browse/PROJ-123) — use this when referencing the ticket"},
				{Name: "fields", Type: "object", Description: "JIRA issue fields in the API's native nested shape — access via fields.*, e.g. fields.summary, fields.status.name, fields.issuetype.name, fields.project.key, fields.assignee.displayName, fields.priority.name, fields.labels. Which keys are present depends on the requested 'fields' input."},
			},
		},
	})

	// 5.2 search_issues
	t.RegisterCapability(core.Capability{
		Name: "search_issues",

		Description: "Searches JIRA issues using JQL. " +
			"Use when you need to find multiple issues by project, status, assignee, or any field. " +
			"Returns: list of matching issues with key, summary, status, and requested fields.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleSearchIssues,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "jql", Type: "string", Example: "project = MYPROJ AND status = 'To Do'",
					Description: "JQL query. Must include a bounded filter like 'project = KEY'"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "fields", Type: "string", Example: "summary,status,assignee,priority",
					Description: "Comma-separated field names to return per issue"},
				{Name: "max_results", Type: "number", Example: "50",
					Description: "Max results to return (1-100, default 50)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issues", Type: "array", Description: "List of matching issues, each with top-level key, id, browse_url, and a fields object (summary, status, and any requested fields, in JIRA's native nested shape — e.g. fields.status.name)"},
				{Name: "total", Type: "number", Example: "0", Description: "Total number of matching issues"},
				{Name: "max_results", Type: "number", Example: "50", Description: "Maximum results returned"},
				{Name: "jql", Type: "string", Description: "JQL query that was executed"},
			},
		},
	})

	// 5.3 create_issue
	t.RegisterCapability(core.Capability{
		Name: "create_issue",

		Description: "Creates a new JIRA issue in a project. " +
			"Use when you need to file a bug, task, story, or epic. " +
			"Returns: created issue key and ID.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleCreateIssue,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "project_key", Type: "string", Example: "PROJ",
					Description: "Target project key"},
				{Name: "summary", Type: "string", Example: "Login page returns 500 error",
					Description: "Issue title"},
				{Name: "issue_type", Type: "string", Example: "Bug",
					Description: "One of: Bug, Task, Story, Epic, Sub-task"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "description", Type: "string", Example: "Steps to reproduce: 1. Go to /login 2. Click submit",
					Description: "Plain text description (auto-converted to ADF)"},
				{Name: "assignee_account_id", Type: "string", Example: "5b10ac8d82e05b22cc7d4ef5",
					Description: "Atlassian account ID of the assignee"},
				{Name: "priority", Type: "string", Example: "High",
					Description: "One of: Highest, High, Medium, Low, Lowest"},
				{Name: "labels", Type: "string", Example: "backend,urgent",
					Description: "Comma-separated labels"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "key", Type: "string", Description: "Created issue key (e.g. PROJ-124)"},
				{Name: "browse_url", Type: "string", Description: "Human-facing browse link for the created issue (e.g. https://your-domain.atlassian.net/browse/PROJ-124) — use this when referencing the ticket"},
				{Name: "id", Type: "string", Description: "Created issue numeric ID"},
				{Name: "self", Type: "string", Description: "REST API URL of the created issue"},
				{Name: "summary", Type: "string", Description: "Issue title as created"},
				{Name: "project_key", Type: "string", Description: "Project key"},
				{Name: "issue_type", Type: "string", Description: "Issue type"},
			},
		},
	})

	// 5.4 update_issue
	t.RegisterCapability(core.Capability{
		Name: "update_issue",

		Description: "Updates fields on an existing JIRA issue. " +
			"Use when you need to change summary, description, priority, or labels. " +
			"Do NOT use for status changes (use transition_issue) or assignment (use assign_issue). " +
			"Returns: success confirmation.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleUpdateIssue,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Example: "PROJ-123",
					Description: "Issue key to update"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "summary", Type: "string", Example: "Updated issue title",
					Description: "New title"},
				{Name: "description", Type: "string", Example: "Updated description text",
					Description: "New plain text description (auto-converted to ADF)"},
				{Name: "priority", Type: "string", Example: "High",
					Description: "One of: Highest, High, Medium, Low, Lowest"},
				{Name: "add_labels", Type: "string", Example: "critical,p0",
					Description: "Comma-separated labels to add"},
				{Name: "remove_labels", Type: "string", Example: "backlog",
					Description: "Comma-separated labels to remove"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Description: "Issue key that was updated"},
				{Name: "message", Type: "string", Description: "Human-readable confirmation message"},
			},
		},
	})

	// 5.5 add_comment
	t.RegisterCapability(core.Capability{
		Name: "add_comment",

		Description: "Adds a comment to a JIRA issue. " +
			"Use when you need to post an update, note, or discussion on an issue. " +
			"Returns: comment ID, author, and timestamp.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleAddComment,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Example: "PROJ-123",
					Description: "Issue key to comment on"},
				{Name: "body", Type: "string", Example: "Fixed in commit abc123. Ready for QA.",
					Description: "Comment text in plain text (auto-converted to ADF)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "comment_id", Type: "string", Description: "Created comment ID"},
				{Name: "issue_key", Type: "string", Description: "Issue the comment was added to"},
				{Name: "created", Type: "string", Description: "Comment creation timestamp"},
			},
		},
	})

	// 5.6 transition_issue
	t.RegisterCapability(core.Capability{
		Name: "transition_issue",

		Description: "Changes a JIRA issue's workflow status. " +
			"Use when you need to move an issue through stages (e.g. To Do -> In Progress -> Done). " +
			"Automatically fetches available transitions and matches by name. " +
			"Returns: success confirmation with the applied transition.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleTransitionIssue,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Example: "PROJ-123",
					Description: "Issue key to transition"},
				{Name: "transition_name", Type: "string", Example: "In Progress",
					Description: "Target status name, e.g. 'To Do', 'In Progress', 'Done' (case-insensitive)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Description: "Issue key that was transitioned"},
				{Name: "transition_name", Type: "string", Description: "Name of the applied transition"},
				{Name: "target_status", Type: "string", Description: "Workflow status the issue moved to"},
				{Name: "message", Type: "string", Description: "Human-readable confirmation message"},
			},
		},
	})

	// 5.7 assign_issue
	t.RegisterCapability(core.Capability{
		Name: "assign_issue",

		Description: "Assigns or unassigns a JIRA issue. " +
			"Use when you need to change who is responsible for an issue. " +
			"Use AFTER lookup_user to resolve a display name to an account ID. " +
			"Returns: success confirmation.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleAssignIssue,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Example: "PROJ-123",
					Description: "Issue key to assign"},
				{Name: "account_id", Type: "string", Example: "5b10ac8d82e05b22cc7d4ef5",
					Description: "Atlassian account ID. Use empty string to unassign"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Description: "Issue key that was assigned or unassigned"},
				{Name: "action", Type: "string", Description: "Action performed (assigned or unassigned)"},
				{Name: "message", Type: "string", Description: "Human-readable confirmation message"},
			},
		},
	})

	// 5.8 lookup_user
	t.RegisterCapability(core.Capability{
		Name: "lookup_user",

		Description: "Searches for JIRA users by display name or email address. " +
			"Use when you need to find a user's Atlassian account ID for assignment or other operations. " +
			"Use BEFORE assign_issue or create_issue when only a person's name is known. " +
			"Returns: list of matching users with account_id, display_name, email, and active status.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleLookupUser,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "query", Type: "string", Example: "John Smith",
					Description: "Search query: display name, email address, or partial match"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "max_results", Type: "number", Example: "10",
					Description: "Max users to return (1-50, default 10)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "users", Type: "array", Description: "List of matching users with account_id, display_name, email, and active status"},
				{Name: "total", Type: "number", Description: "Number of matching users returned"},
			},
		},
	})

	// 5.9 get_project
	t.RegisterCapability(core.Capability{
		Name: "get_project",

		Description: "Gets JIRA project details by key or ID. " +
			"Use when you need project metadata like available issue types, components, or project lead. " +
			"Returns: project name, key, lead, issue types, and components.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetProject,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "project_key", Type: "string", Example: "PROJ",
					Description: "Project key or numeric ID"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "key", Type: "string", Description: "Project key"},
				{Name: "name", Type: "string", Description: "Project name"},
				{Name: "lead", Type: "string", Description: "Project lead display name"},
				{Name: "issue_types", Type: "array", Description: "Available issue types in the project"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "components", Type: "array", Description: "Project components"},
			},
		},
	})

	// 5.10 list_boards
	t.RegisterCapability(core.Capability{
		Name: "list_boards",

		Description: "Lists agile boards in JIRA, optionally filtered by project. " +
			"Use when you need to find a board ID for sprint operations. " +
			"Use BEFORE list_sprints to discover available boards. " +
			"Returns: list of boards with ID, name, type (scrum/kanban), and project info.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleListBoards,

		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "project_key", Type: "string", Example: "PROJ",
					Description: "Filter boards by project key. Omit to list all accessible boards"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "boards", Type: "array", Description: "List of boards with id, name, type (scrum/kanban), and project info"},
				{Name: "total", Type: "number", Description: "Total number of boards returned"},
			},
		},
	})

	// 5.11 list_sprints
	t.RegisterCapability(core.Capability{
		Name: "list_sprints",

		Description: "Lists sprints for a JIRA agile board. " +
			"Use when you need to see active, future, or closed sprints for sprint planning or status checks. " +
			"Use AFTER list_boards to find the board ID. " +
			"Returns: list of sprints with ID, name, state, dates, and goal.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleListSprints,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "board_id", Type: "number", Example: "42",
					Description: "Agile board ID"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "state", Type: "string", Example: "active",
					Description: "Filter by sprint state: active, closed, or future. Omit for all sprints"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "sprints", Type: "array", Description: "List of sprints with id, name, state, start_date, end_date, and goal"},
				{Name: "total", Type: "number", Description: "Total number of sprints returned"},
			},
		},
	})

	// 5.11 get_sprint_issues
	t.RegisterCapability(core.Capability{
		Name: "get_sprint_issues",

		Description: "Gets all issues in a specific JIRA sprint. " +
			"Use when you need to review sprint contents, check progress, or do sprint planning. " +
			"Use AFTER list_sprints to find the sprint ID. " +
			"Returns: list of issues with key, summary, status, and requested fields.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetSprintIssues,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "sprint_id", Type: "number", Example: "42",
					Description: "Sprint ID (from list_sprints)"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "fields", Type: "string", Example: "summary,status,assignee,priority",
					Description: "Comma-separated field names to return per issue"},
				{Name: "max_results", Type: "number", Example: "50",
					Description: "Max results to return (1-100, default 50)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issues", Type: "array", Description: "List of sprint issues, each with top-level key, id, browse_url, and a fields object (summary, status, and requested fields, in JIRA's native nested shape — e.g. fields.status.name)"},
				{Name: "total", Type: "number", Example: "0", Description: "Total number of issues in the sprint"},
				{Name: "sprint_id", Type: "number", Example: "1", Description: "Sprint ID queried"},
			},
		},
	})

	// 5.12 add_worklog
	t.RegisterCapability(core.Capability{
		Name: "add_worklog",

		Description: "Logs work time on a JIRA issue for time tracking. " +
			"Use when you need to record hours spent on an issue. " +
			"Time tracking must be enabled in the JIRA project. " +
			"Returns: worklog ID, time spent, and timestamps.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleAddWorklog,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Example: "PROJ-123",
					Description: "Issue key to log work against"},
				{Name: "time_spent", Type: "string", Example: "2h 30m",
					Description: "Time spent in human format: e.g. '2h', '30m', '1h 30m', '1d'"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "started", Type: "string", Example: "2024-01-15T09:00:00.000+0000",
					Description: "When the work started (ISO 8601). Defaults to now"},
				{Name: "comment", Type: "string", Example: "Investigated and fixed the memory leak",
					Description: "Work description (plain text, auto-converted to ADF)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "worklog_id", Type: "string", Description: "Created worklog entry ID"},
				{Name: "issue_key", Type: "string", Description: "Issue the worklog was added to"},
				{Name: "time_spent", Type: "string", Description: "Time spent as recorded (e.g. 2h)"},
				{Name: "time_spent_seconds", Type: "number", Example: "7200", Description: "Time spent in seconds"},
				{Name: "started", Type: "string", Description: "When the work started"},
				{Name: "created", Type: "string", Description: "Worklog creation timestamp"},
			},
		},
	})

	// 5.13 link_issues
	t.RegisterCapability(core.Capability{
		Name: "link_issues",

		Description: "Creates a link between two JIRA issues for dependency tracking. " +
			"Use when you need to mark issues as blocking, duplicating, or relating to each other. " +
			"Auto-validates the link type against available types in the JIRA instance. " +
			"Returns: success confirmation with link details.",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleLinkIssues,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "link_type", Type: "string", Example: "Blocks",
					Description: "Link type name, e.g. 'Blocks', 'Duplicate', 'Relates' (case-insensitive, auto-validated)"},
				{Name: "inward_key", Type: "string", Example: "PROJ-100",
					Description: "Inward issue key (e.g. 'is blocked by' side)"},
				{Name: "outward_key", Type: "string", Example: "PROJ-200",
					Description: "Outward issue key (e.g. 'blocks' side)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "link_type", Type: "string", Description: "Link type that was created"},
				{Name: "inward_key", Type: "string", Description: "Inward issue key"},
				{Name: "outward_key", Type: "string", Description: "Outward issue key"},
				{Name: "message", Type: "string", Description: "Human-readable confirmation message"},
			},
		},
	})

	// 5.14 get_changelog
	t.RegisterCapability(core.Capability{
		Name: "get_changelog",

		Description: "Gets the change history of a JIRA issue. " +
			"Use when you need to audit what changed, when, and by whom on an issue. " +
			"Shows field changes like status transitions, assignee changes, priority updates, etc. " +
			"Returns: list of change entries with author, timestamp, and field changes (from/to values).",

		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     t.handleGetChangelog,

		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Example: "PROJ-123",
					Description: "Issue key to get changelog for"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "max_results", Type: "number", Example: "50",
					Description: "Max changelog entries to return (1-100, default 50)"},
			},
		},

		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "issue_key", Type: "string", Description: "Issue key the changelog belongs to"},
				{Name: "histories", Type: "array", Description: "List of changelog entries, each with author, timestamp, and field changes (from/to values)"},
				{Name: "total", Type: "number", Example: "0", Description: "Total number of changelog entries"},
			},
		},
	})
}
