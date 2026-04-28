/**
 * DOM utilities.
 *
 * Event delegation, debouncing, and throttling helpers.
 * Pure functions with no dependencies.
 */

/**
 * Attach a delegated event listener to a container.
 * Instead of inline onclick on each element, one listener on the container
 * matches events against a CSS selector via closest().
 *
 * @param {HTMLElement} container
 * @param {string} eventType - e.g., 'click'
 * @param {string} selector - CSS selector for target elements
 * @param {Function} handler - (event, matchedElement) => void
 * @returns {Function} cleanup function to remove the listener
 */
export function delegate(container, eventType, selector, handler) {
    const listener = (event) => {
        const target = event.target.closest(selector);
        if (target && container.contains(target)) {
            handler(event, target);
        }
    };
    container.addEventListener(eventType, listener);
    return () => container.removeEventListener(eventType, listener);
}

/**
 * Debounce a function — delays execution until the function stops
 * being called for `ms` milliseconds.
 *
 * @param {Function} fn
 * @param {number} ms - delay in milliseconds (default 150)
 * @returns {Function} debounced function with .cancel() method
 */
export function debounce(fn, ms = 150) {
    let timer;
    const debounced = (...args) => {
        clearTimeout(timer);
        timer = setTimeout(() => fn(...args), ms);
    };
    debounced.cancel = () => clearTimeout(timer);
    return debounced;
}

/**
 * Throttle a function using requestAnimationFrame — ensures it runs
 * at most once per animation frame (~16ms at 60fps).
 *
 * @param {Function} fn
 * @returns {Function} throttled function
 */
export function throttle(fn) {
    let scheduled = false;
    return (...args) => {
        if (scheduled) return;
        scheduled = true;
        requestAnimationFrame(() => {
            fn(...args);
            scheduled = false;
        });
    };
}

/**
 * Show a loading indicator on a container element.
 * Adds a semi-transparent overlay with a spinner.
 * Call hideLoading() on the same container to remove it.
 *
 * @param {HTMLElement} container - the element to overlay
 * @param {string} [message='Loading...'] - optional message text
 */
export function showLoading(container, message = 'Loading...') {
    if (!container) return;
    // Don't add duplicate overlays
    if (container.querySelector('.loading-overlay')) return;
    const overlay = document.createElement('div');
    overlay.className = 'loading-overlay';
    overlay.innerHTML = `
        <div class="loading-spinner">
            <span class="loading-spin-icon">↻</span>
            <span class="loading-text">${message}</span>
        </div>
    `;
    container.style.position = container.style.position || 'relative';
    container.appendChild(overlay);
}

/**
 * Remove the loading indicator from a container.
 *
 * @param {HTMLElement} container
 */
export function hideLoading(container) {
    if (!container) return;
    const overlay = container.querySelector('.loading-overlay');
    if (overlay) overlay.remove();
}
