/**
 * Centralized State Management.
 *
 * Replaces 34 global variables (index.html lines 335-376, 702) with a single
 * state tree and path-based subscriptions. Views subscribe to their slice and
 * only re-render when their data changes.
 *
 * cyInstance is excluded — it's a DOM reference (Cytoscape instance), not
 * serializable data. Managed directly by the DAG view module.
 */

const state = {
    currentView: 'registry',
    autoRefresh: true,

    registry: {
        services: [],
        selected: null,
        activeTab: 'formatted',
        typeFilter: 'all',
    },

    llmDebug: {
        records: [],
        selected: null,
        activeTab: 'interactions',
        typeFilter: 'all',
        expandedInteractions: new Set(),
        conversationFilter: null,
    },

    hitl: {
        checkpoints: [],
        selected: null,
        activeTab: 'overview',
        typeFilter: 'all',
        sortColumn: 'expires_at',
        sortDirection: 'asc',
    },

    dag: {
        executions: [],
        selected: null,
        activeTab: 'dag-viz',
        filter: 'all',
        viewMode: 'full',
        sortColumn: 'created_at',
        sortDirection: 'desc',
    },

    memory: {
        domain: localStorage.getItem('memoryDomain') || '',
        timeRange: '24h',
        events: [],
        investigations: [],
        activities: [],
        digest: null,
        selectedEvent: null,
        activeTab: 'mem-event',
    },
};

/** @type {Map<string, Set<Function>>} */
const listeners = new Map();

/**
 * Get a value from state at a dot-separated path.
 * @param {string} path - e.g., 'dag.selected', 'llmDebug.records'
 * @returns {*}
 */
export function getState(path) {
    return path.split('.').reduce((obj, key) => obj?.[key], state);
}

/**
 * Set a value in state at a dot-separated path and notify subscribers.
 * Notifies listeners on the exact path and all parent paths.
 * @param {string} path - e.g., 'dag.selected', 'registry.services'
 * @param {*} value
 */
export function setState(path, value) {
    const keys = path.split('.');
    const last = keys.pop();
    const parent = keys.reduce((obj, key) => obj[key], state);
    if (parent === undefined) {
        console.warn(`setState: invalid path "${path}"`);
        return;
    }
    parent[last] = value;

    // Notify exact-path listeners
    notify(path, value);

    // Notify parent-path listeners (e.g., 'dag' when 'dag.selected' changes)
    let partial = '';
    for (const key of path.split('.').slice(0, -1)) {
        partial = partial ? `${partial}.${key}` : key;
        const parentValue = getState(partial);
        notify(partial, parentValue);
    }
}

/**
 * Subscribe to state changes at a path. Returns an unsubscribe function.
 * The callback receives (newValue, path).
 * @param {string} path
 * @param {Function} callback - (newValue, path) => void
 * @returns {Function} unsubscribe
 */
export function subscribe(path, callback) {
    if (!listeners.has(path)) {
        listeners.set(path, new Set());
    }
    listeners.get(path).add(callback);
    return () => listeners.get(path)?.delete(callback);
}

/**
 * Notify listeners for a specific path.
 * @param {string} path
 * @param {*} value
 */
function notify(path, value) {
    const pathListeners = listeners.get(path);
    if (pathListeners) {
        for (const cb of pathListeners) {
            try {
                cb(value, path);
            } catch (err) {
                console.error(`State listener error on "${path}":`, err);
            }
        }
    }
}
