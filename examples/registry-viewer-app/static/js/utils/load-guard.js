/** Per-view request ownership. Abort is best effort; generations reject late results. */
export function createLoadGuard() {
    let generation = 0;
    let controller;
    return {
        begin() {
            controller?.abort();
            controller = new AbortController();
            return { generation: ++generation, signal: controller.signal };
        },
        current(ticket) { return ticket.generation === generation && !ticket.signal.aborted; },
        cancel() { generation++; controller?.abort(); controller = undefined; },
    };
}
