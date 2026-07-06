(() => {
  'use strict';

  const maxEvents = 250;
  const maxReconnectDelay = 30000;
  const state = {
    events: [],
    totalEvents: 0,
    mode: 'terminal',
    ws: null,
    reconnectDelay: 1000,
    reconnectTimer: null,
    reconnectAttempts: 0,
  };

  const els = {
    wsStatus: document.getElementById('ws-status'),
    eventCount: document.getElementById('event-count'),
    modeToggle: document.getElementById('mode-toggle'),
    terminalModeLabel: document.getElementById('terminal-mode-label'),
    timelineModeLabel: document.getElementById('timeline-mode-label'),
    telemetryFeed: document.getElementById('telemetry-feed'),
    mycorrhizal: document.getElementById('mycorrhizal-network'),
    thoughtStream: document.getElementById('thought-stream'),
    rhizomeFeed: document.getElementById('rhizome-feed'),
    triggerFeed: document.getElementById('trigger-feed'),
    sproutGarden: document.getElementById('sprout-garden'),
    xylemTerminal: document.getElementById('xylem-terminal'),
    xylemOutput: document.getElementById('xylem-output'),
    dangerOverlay: document.getElementById('danger-overlay'),
  };

  function connect() {
    const wsUrl = buildWebSocketUrl();

    try {
      state.ws = new WebSocket(wsUrl);
    } catch (err) {
      console.warn('Unable to create WebSocket:', err);
      setConnectionStatus(false);
      scheduleReconnect();
      return;
    }

    state.ws.addEventListener('open', () => {
      setConnectionStatus(true);
      state.reconnectDelay = 1000;
      state.reconnectAttempts = 0;
    });

    state.ws.addEventListener('close', () => {
      setConnectionStatus(false);
      scheduleReconnect();
    });

    state.ws.addEventListener('error', () => {
      setConnectionStatus(false);
    });

    state.ws.addEventListener('message', (evt) => {
      try {
        handleEvent(JSON.parse(evt.data));
      } catch (err) {
        console.warn('Invalid WebSocket payload:', err, evt.data);
      }
    });
  }

  function buildWebSocketUrl() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const host = window.location.host || 'localhost:9999';
    return `${protocol}//${host}/ws`;
  }

  function scheduleReconnect() {
    if (state.reconnectTimer) {
      return;
    }

    const jitter = Math.floor(Math.random() * 350);
    const delay = Math.min(state.reconnectDelay + jitter, maxReconnectDelay);
    state.reconnectAttempts += 1;
    els.wsStatus.textContent = `Reconnecting ${state.reconnectAttempts}`;

    state.reconnectTimer = window.setTimeout(() => {
      state.reconnectTimer = null;
      state.reconnectDelay = Math.min(state.reconnectDelay * 2, maxReconnectDelay);
      connect();
    }, delay);
  }

  function setConnectionStatus(connected) {
    els.wsStatus.textContent = connected ? 'Connected' : 'Disconnected';
    els.wsStatus.className = `status-pill ${connected ? 'status-connected' : 'status-disconnected'}`;
  }

  function handleEvent(payload) {
    if (!payload || !payload.type) {
      return;
    }

    const event = normalizeEvent(payload);
    if (event.type === 'connected') {
      setConnectionStatus(true);
      return;
    }

    state.events.push(event);
    state.totalEvents += 1;
    if (state.events.length > maxEvents) {
      state.events.shift();
    }

    els.eventCount.textContent = `Events: ${state.totalEvents}`;
    renderTelemetry();
    routeLegacyPanels(event);
  }

  function normalizeEvent(payload) {
    return {
      type: String(payload.type),
      timestamp: payload.timestamp || new Date().toISOString(),
      source: payload.source || 'orchestrator',
      data: payload.data ?? payload.content ?? {},
      raw: payload,
    };
  }

  function renderTelemetry() {
    if (state.events.length === 0) {
      renderEmptyState();
      return;
    }

    if (state.mode === 'timeline') {
      renderTimeline();
      return;
    }

    renderTerminal();
  }

  function renderEmptyState() {
    els.telemetryFeed.className = 'telemetry-feed';
    els.telemetryFeed.innerHTML = `
      <div class="empty-state">
        <span class="empty-orb" aria-hidden="true"></span>
        <p>Awaiting telemetry from the Go orchestrator at <code>/ws</code>.</p>
      </div>
    `;
  }

  function renderTerminal() {
    const shouldStickToBottom = isScrolledNearBottom(els.telemetryFeed);
    const fragment = document.createDocumentFragment();

    state.events.forEach((event) => {
      const card = document.createElement('article');
      card.className = `event-card ${getEventClass(event.type)}`;

      const meta = document.createElement('div');
      meta.className = 'event-meta';

      const time = document.createElement('span');
      time.className = 'event-time';
      time.textContent = formatTime(event.timestamp);

      const type = document.createElement('strong');
      type.className = 'event-type';
      type.textContent = event.type;

      const source = document.createElement('span');
      source.className = 'event-source';
      source.textContent = event.source;

      const payload = document.createElement('pre');
      payload.className = 'event-payload';
      payload.textContent = formatPayload(event.data);

      meta.append(time, type, source);
      card.append(meta, payload);
      fragment.appendChild(card);
    });

    els.telemetryFeed.className = 'telemetry-feed terminal';
    els.telemetryFeed.replaceChildren(fragment);

    if (shouldStickToBottom) {
      scrollTelemetryToBottom();
    }
  }

  function renderTimeline() {
    const shouldStickToBottom = isScrolledNearBottom(els.telemetryFeed);
    const timeline = document.createElement('div');
    timeline.className = 'timeline';

    state.events.forEach((event, index) => {
      const item = document.createElement('article');
      item.className = `timeline-item ${index % 2 === 0 ? 'left' : 'right'} ${getEventClass(event.type)}`;

      const node = document.createElement('span');
      node.className = 'timeline-node';
      node.setAttribute('aria-hidden', 'true');

      const card = document.createElement('div');
      card.className = 'timeline-card';

      const time = document.createElement('span');
      time.className = 'timeline-time';
      time.textContent = formatTime(event.timestamp);

      const type = document.createElement('strong');
      type.className = 'timeline-type';
      type.textContent = event.type;

      const summary = document.createElement('p');
      summary.className = 'timeline-summary';
      summary.textContent = summarizeEvent(event);

      card.append(time, type, summary);
      item.append(node, card);
      timeline.appendChild(item);
    });

    els.telemetryFeed.className = 'telemetry-feed timeline-mode';
    els.telemetryFeed.replaceChildren(timeline);

    if (shouldStickToBottom) {
      scrollTelemetryToBottom();
    }
  }

  function routeLegacyPanels(event) {
    switch (event.type) {
      case 'EventStreamToken':
      case 'stream-token':
      case 'stream.token':
        handleStreamToken(event);
        break;
      case 'EventThoughtBranch':
      case 'thought-branch':
        handleThoughtBranch(event);
        break;
      case 'EventSequenceComplete':
      case 'sprout-emerged':
        handleSproutEmerged(event);
        break;
      case 'EventError':
      case 'hormonal-trigger':
        handleHormonalTrigger(event);
        break;
      case 'rhizome-update':
        handleRhizomeUpdate(event);
        break;
      case 'xylem-transport':
        handleXylemTransport(event);
        break;
      default:
        appendGenericEvent(event);
        break;
    }
  }

  function handleStreamToken(event) {
    const token = extractContent(event);
    const dataType = event.data && event.data.type;

    if (dataType === 'stream.end') {
      const endContent = event.data.content || token || '';
      if (endContent) {
        appendXylemText(`\n[phloem complete] ${endContent}\n`);
      }
      els.xylemTerminal.classList.remove('flowing');
      return;
    }

    if (dataType === 'stream.start') {
      clearXylem();
      appendXylemText('[xylem flow initiated]\n');
      els.xylemTerminal.classList.add('flowing');
      ensureSprout(event.source || 'sprout');
      return;
    }

    if (token) {
      appendXylemText(token);
      els.xylemTerminal.classList.add('flowing');
      window.setTimeout(() => {
        els.xylemTerminal.classList.remove('flowing');
      }, 400);
    }
  }

  function handleThoughtBranch(event) {
    const thought = extractContent(event);
    if (!thought) {
      return;
    }

    clearPlaceholder(els.thoughtStream);

    const entry = document.createElement('div');
    entry.className = 'thought-entry';
    entry.textContent = thought;
    els.thoughtStream.prepend(entry);

    els.mycorrhizal.classList.add('pulse-active');
    window.setTimeout(() => {
      els.mycorrhizal.classList.remove('pulse-active');
    }, 1200);

    trimChildren(els.thoughtStream, 12);
  }

  function handleSproutEmerged(event) {
    ensureSprout(event.source || `sprout-${Date.now()}`, event.data);
  }

  function handleHormonalTrigger(event) {
    document.body.classList.add('danger-flash');
    els.dangerOverlay.classList.add('active');

    clearPlaceholder(els.triggerFeed, '.panel-placeholder');

    const item = document.createElement('li');
    const message = extractContent(event) || event.data.message || 'Growth inhibited';
    item.textContent = `[${formatTime(event.timestamp)}] ${message}`;
    els.triggerFeed.prepend(item);

    window.setTimeout(() => {
      document.body.classList.remove('danger-flash');
      els.dangerOverlay.classList.remove('active');
    }, 600);

    trimChildren(els.triggerFeed, 8);
  }

  function handleRhizomeUpdate(event) {
    clearPlaceholder(els.rhizomeFeed, '.panel-placeholder');

    const item = document.createElement('li');
    const detail = event.data.summary || event.data.action || event.source || 'index mutation';
    item.textContent = `[${formatTime(event.timestamp)}] ${detail}`;
    els.rhizomeFeed.prepend(item);

    trimChildren(els.rhizomeFeed, 10);
  }

  function handleXylemTransport(event) {
    const content = extractContent(event) || formatPayload(event.data);
    appendXylemText(`[xylem] ${content}\n`);
    els.xylemTerminal.classList.add('flowing');
    window.setTimeout(() => {
      els.xylemTerminal.classList.remove('flowing');
    }, 800);
  }

  function appendGenericEvent(event) {
    appendXylemText(`[${event.type}] ${event.source || ''}\n`);
  }

  function ensureSprout(id, data = {}) {
    if (!id) {
      return;
    }

    const safeId = `sprout-${cssSafeId(id)}`;
    const existing = document.getElementById(safeId);
    if (existing) {
      existing.classList.add('active');
      return;
    }

    clearPlaceholder(els.sproutGarden);

    const node = document.createElement('div');
    node.className = 'sprout-node active';
    node.id = safeId;

    const icon = document.createElement('span');
    icon.className = 'sprout-icon';
    icon.textContent = '▲';
    icon.setAttribute('aria-hidden', 'true');

    const label = document.createElement('span');
    label.className = 'sprout-label';
    label.textContent = data.label || id;

    node.append(icon, label);
    els.sproutGarden.appendChild(node);
  }

  function appendXylemText(text) {
    if (!text) {
      return;
    }

    const span = document.createElement('span');
    span.className = 'xylem-token';
    span.textContent = text;
    els.xylemOutput.appendChild(span);
    els.xylemTerminal.scrollTop = els.xylemTerminal.scrollHeight;
  }

  function clearXylem() {
    els.xylemOutput.textContent = '';
  }

  function clearPlaceholder(container, selector = '.panel-placeholder') {
    const placeholder = container.querySelector(selector);
    if (placeholder) {
      placeholder.remove();
    }
  }

  function trimChildren(container, limit) {
    while (container.children.length > limit) {
      container.removeChild(container.lastChild);
    }
  }

  function extractContent(event) {
    if (typeof event.data === 'string') {
      return event.data;
    }

    if (event.raw.content) {
      return String(event.raw.content);
    }

    if (event.data) {
      if (event.data.token) {
        return String(event.data.token);
      }
      if (event.data.thought) {
        return String(event.data.thought);
      }
      if (event.data.content) {
        return String(event.data.content);
      }
      if (event.data.message) {
        return String(event.data.message);
      }
    }

    return '';
  }

  function formatPayload(data) {
    if (data === null || data === undefined || data === '') {
      return '{}';
    }

    if (typeof data === 'string') {
      return data;
    }

    try {
      return JSON.stringify(data, null, 2);
    } catch (err) {
      return String(data);
    }
  }

  function summarizeEvent(event) {
    const content = extractContent(event);
    if (content) {
      return truncate(content, 180);
    }

    if (typeof event.data === 'object' && event.data !== null) {
      const summary = event.data.summary || event.data.message || event.data.action || event.data.status;
      if (summary) {
        return truncate(String(summary), 180);
      }
    }

    return truncate(formatPayload(event.data), 180);
  }

  function truncate(value, length) {
    if (value.length <= length) {
      return value;
    }
    return `${value.slice(0, length - 1)}…`;
  }

  function getEventClass(type) {
    const normalized = type.replace(/([a-z0-9])([A-Z])/g, '$1-$2').replace(/[._\s]+/g, '-').toLowerCase();

    if (normalized === 'event-sequence-complete' || normalized === 'sprout-emerged') {
      return 'event-sequence-complete';
    }
    if (normalized === 'event-thought-branch' || normalized === 'thought-branch') {
      return 'event-thought-branch';
    }
    if (normalized === 'event-error' || normalized === 'hormonal-trigger') {
      return normalized === 'hormonal-trigger' ? 'event-hormonal-trigger' : 'event-error';
    }
    if (normalized === 'event-stream-token' || normalized === 'stream-token') {
      return 'event-stream-token';
    }

    return `event-${normalized}`;
  }

  function formatTime(timestamp) {
    const date = new Date(timestamp || Date.now());
    if (Number.isNaN(date.getTime())) {
      return String(timestamp);
    }

    return date.toLocaleTimeString([], {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    });
  }

  function isScrolledNearBottom(element) {
    return element.scrollHeight - element.scrollTop - element.clientHeight < 80;
  }

  function scrollTelemetryToBottom() {
    els.telemetryFeed.scrollTop = els.telemetryFeed.scrollHeight;
  }

  function cssSafeId(value) {
    return String(value).replace(/[^a-zA-Z0-9-]/g, '-');
  }

  function bindModeToggle() {
    els.modeToggle.addEventListener('change', () => {
      state.mode = els.modeToggle.checked ? 'timeline' : 'terminal';
      els.terminalModeLabel.classList.toggle('active', state.mode === 'terminal');
      els.timelineModeLabel.classList.toggle('active', state.mode === 'timeline');
      renderTelemetry();
      scrollTelemetryToBottom();
    });
  }

  bindModeToggle();
  connect();
})();
