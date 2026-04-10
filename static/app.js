/**
 * Tendril — Client-side application logic
 * Handles provider loading, input interactions, WebSocket chat, and auto-scroll.
 */

document.addEventListener('DOMContentLoaded', function () {
    initProviderSelector();
    initAutoScroll();
    initInputHandlers();
    initWebSocket();
});

/**
 * Load available LLM providers from the API and populate the selector.
 */
function initProviderSelector() {
    var sel = document.getElementById('provider-select');
    var hidden = document.getElementById('provider-hidden');

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
 * Enhanced input handling: auto-resize textarea, Enter for new line, Ctrl+Enter to send.
 */
function initInputHandlers() {
    var input = document.getElementById('chat-input');
    var form = input.closest('form');

    // Auto-resize textarea
    input.addEventListener('input', function () {
        this.style.height = '50px'; // reset to min-height instead of auto to avoid layout jumps
        var newHeight = Math.min(this.scrollHeight, 200);
        this.style.height = newHeight + 'px';
        if (this.scrollHeight >= 200) {
            this.style.overflowY = 'auto';
        } else {
            this.style.overflowY = 'hidden';
        }
    });

    // Ctrl+Enter to send via WebSocket (preferred) or HTMX fallback
    input.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') {
            if (e.ctrlKey) {
                e.preventDefault();
                if (window.tendrilWS && window.tendrilWS.readyState === WebSocket.OPEN) {
                    sendViaWebSocket();
                } else {
                    htmx.trigger(form, 'submit');
                }
            }
            // Plain Enter: allow default (new line in textarea)
        }
    });

    // Intercept form submit to use WebSocket when available
    form.addEventListener('submit', function (e) {
        if (window.tendrilWS && window.tendrilWS.readyState === WebSocket.OPEN) {
            e.preventDefault();
            sendViaWebSocket();
        }
        // Otherwise let HTMX handle it (SSE fallback)
    });
}

// --- WebSocket Chat ---

var wsReconnectDelay = 1000;
var wsMaxReconnectDelay = 30000;
var currentStreamBubble = null;
var sessionId = 'web-' + Date.now();

/**
 * Connect to the Tendril Chat Gateway via WebSocket.
 * Falls back to HTMX/SSE if gateway is unavailable.
 */
function initWebSocket() {
    // Determine WebSocket URL (same host, port 9090)
    var wsHost = window.location.hostname || 'localhost';
    var wsUrl = 'ws://' + wsHost + ':9090/ws';

    try {
        var ws = new WebSocket(wsUrl);
        window.tendrilWS = ws;

        ws.onopen = function () {
            console.log('🌱 WebSocket connected to gateway');
            wsReconnectDelay = 1000; // Reset backoff
            updateConnectionStatus('ws');
        };

        ws.onmessage = function (event) {
            var msg = JSON.parse(event.data);
            handleWSMessage(msg);
        };

        ws.onclose = function (event) {
            console.log('🔌 WebSocket closed:', event.code);
            window.tendrilWS = null;
            updateConnectionStatus('sse');
            // Reconnect with backoff
            setTimeout(function () {
                wsReconnectDelay = Math.min(wsReconnectDelay * 2, wsMaxReconnectDelay);
                initWebSocket();
            }, wsReconnectDelay);
        };

        ws.onerror = function () {
            console.warn('⚠️ WebSocket error — falling back to SSE');
            updateConnectionStatus('sse');
        };
    } catch (e) {
        console.warn('WebSocket unavailable, using SSE fallback:', e);
        updateConnectionStatus('sse');
    }
}

/**
 * Send a chat message via WebSocket.
 */
function sendViaWebSocket() {
    var input = document.getElementById('chat-input');
    var message = input.value.trim();
    if (!message) return;

    var provider = document.getElementById('provider-hidden').value;

    // Show user message in chat
    appendMessage('user', message);

    // Create streaming bubble for response
    currentStreamBubble = appendMessage('assistant', '');
    currentStreamBubble.innerHTML = '<div class="thinking"><div class="thinking-dot"></div><span>Thinking...</span></div>';

    // Send to gateway
    window.tendrilWS.send(JSON.stringify({
        type: 'message',
        content: message,
        provider: provider,
        session_id: sessionId
    }));

    // Clear input
    input.value = '';
    input.style.height = '50px';
}

/**
 * Handle incoming WebSocket messages from the gateway.
 */
function handleWSMessage(msg) {
    switch (msg.type) {
        case 'connected':
            console.log('✅ Gateway confirmed connection');
            break;

        case 'stream.start':
            if (currentStreamBubble) {
                currentStreamBubble.innerHTML = '';
            }
            break;

        case 'stream.token':
            if (currentStreamBubble) {
                currentStreamBubble.textContent += msg.content;
            }
            break;

        case 'stream.end':
            if (currentStreamBubble && msg.content) {
                // Render final markdown-formatted response
                currentStreamBubble.innerHTML = formatResponse(msg.content);
            }
            currentStreamBubble = null;
            break;

        case 'error':
            if (currentStreamBubble) {
                currentStreamBubble.innerHTML = '<p style="color:var(--danger)">⚠️ ' + escapeHtml(msg.error) + '</p>';
            }
            currentStreamBubble = null;
            break;

        case 'pong':
            break; // Health check response, ignore
    }
}

/**
 * Append a message bubble to the chat container.
 */
function appendMessage(role, content) {
    var container = document.getElementById('chat-messages');

    // Remove welcome message on first chat
    var welcome = container.querySelector('.welcome');
    if (welcome) welcome.remove();

    var row = document.createElement('div');
    row.className = 'msg-row ' + role;

    var bubble = document.createElement('div');
    bubble.className = 'msg-bubble ' + role;
    if (content) {
        bubble.innerHTML = role === 'assistant' ? formatResponse(content) : escapeHtml(content);
    }

    row.appendChild(bubble);
    container.appendChild(row);
    container.scrollTo({ top: container.scrollHeight, behavior: 'smooth' });

    return bubble;
}

/**
 * Basic response formatting: convert markdown-like content to HTML.
 */
function formatResponse(text) {
    // Escape HTML first
    var html = escapeHtml(text);
    // Code blocks
    html = html.replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>');
    // Inline code
    html = html.replace(/`([^`]+)`/g, '<code style="background:var(--bg-tertiary);padding:2px 6px;border-radius:4px;border:1px solid var(--border)">$1</code>');
    // Bold
    html = html.replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>');
    // Paragraphs
    html = html.replace(/\n\n/g, '</p><p>');
    html = '<p>' + html + '</p>';
    return html;
}

function escapeHtml(text) {
    var div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

/**
 * Update the connection status indicator.
 */
function updateConnectionStatus(mode) {
    var dot = document.querySelector('.status-dot');
    if (!dot) return;
    if (mode === 'ws') {
        dot.style.background = 'var(--accent)';
        dot.title = 'Connected via WebSocket (Gateway)';
    } else {
        dot.style.background = 'var(--warning, #f59e0b)';
        dot.title = 'Connected via SSE (Fallback)';
    }
}
