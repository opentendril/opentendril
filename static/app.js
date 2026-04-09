/**
 * Tendril — Client-side application logic
 * Handles provider loading, input interactions, and auto-scroll.
 */

document.addEventListener('DOMContentLoaded', function () {
    initProviderSelector();
    initAutoScroll();
    initInputHandlers();
});

/**
 * Load available LLM providers from the API and populate the selector.
 */
function initProviderSelector() {
    const sel = document.getElementById('provider-select');
    const hidden = document.getElementById('provider-hidden');

    // Sync selector with hidden form field
    sel.addEventListener('change', function () {
        hidden.value = sel.value;
    });

    // Fetch available providers from API
    fetch('/api/providers')
        .then(function (res) { return res.json(); })
        .then(function (providers) {
            providers.forEach(function (p) {
                var opt = document.createElement('option');
                opt.value = p.value;
                opt.textContent = p.label;
                sel.appendChild(opt);
            });
        })
        .catch(function (err) {
            console.warn('Failed to load providers:', err);
        });
}

/**
 * Auto-scroll chat messages when new content appears.
 */
function initAutoScroll() {
    var container = document.getElementById('chat-messages');
    var observer = new MutationObserver(function () {
        container.scrollTo({ top: container.scrollHeight, behavior: 'smooth' });
    });
    observer.observe(container, { childList: true, subtree: true });
}

/**
 * Enhanced input handling: auto-resize textarea and Ctrl+Enter to send.
 */
function initInputHandlers() {
    var input = document.getElementById('chat-input');
    var form = input.closest('form');

    // Auto-resize textarea
    input.addEventListener('input', function () {
        this.style.height = '50px';
        this.style.height = this.scrollHeight + 'px';
    });

    // Ctrl+Enter to submit
    input.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' && e.ctrlKey) {
            e.preventDefault();
            htmx.trigger(form, 'submit');
        }
    });
}
