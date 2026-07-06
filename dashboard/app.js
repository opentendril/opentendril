(function () {
  'use strict';

  var eventCount = 0;
  var ws = null;
  var reconnectDelay = 1000;
  var maxReconnectDelay = 15000;

  var els = {
    wsStatus: document.getElementById('ws-status'),
    eventCount: document.getElementById('event-count'),
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
    var protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(protocol + '//' + window.location.host + '/ws');

    ws.onopen = function () {
      setConnectionStatus(true);
      reconnectDelay = 1000;
    };

    ws.onclose = function () {
      setConnectionStatus(false);
      scheduleReconnect();
    };

    ws.onerror = function () {
      setConnectionStatus(false);
    };

    ws.onmessage = function (evt) {
      try {
        var payload = JSON.parse(evt.data);
        handleEvent(payload);
      } catch (err) {
        console.warn('Invalid WebSocket payload:', err);
      }
    };
  }

  function scheduleReconnect() {
    setTimeout(function () {
      connect();
      reconnectDelay = Math.min(reconnectDelay * 1.5, maxReconnectDelay);
    }, reconnectDelay);
  }

  function setConnectionStatus(connected) {
    els.wsStatus.textContent = connected ? 'Connected' : 'Disconnected';
    els.wsStatus.className = 'status-pill ' + (connected ? 'status-connected' : 'status-disconnected');
  }

  function incrementEventCount() {
    eventCount += 1;
    els.eventCount.textContent = 'Events: ' + eventCount;
  }

  function clearPlaceholder(container, selector) {
    var placeholder = container.querySelector(selector || '.panel-placeholder');
    if (placeholder) {
      placeholder.remove();
    }
  }

  function handleEvent(payload) {
    if (!payload || !payload.type) {
      return;
    }

    if (payload.type === 'connected') {
      setConnectionStatus(true);
      return;
    }

    incrementEventCount();

    switch (payload.type) {
      case 'stream-token':
      case 'stream.token':
        handleStreamToken(payload);
        break;
      case 'thought-branch':
        handleThoughtBranch(payload);
        break;
      case 'sprout-emerged':
        handleSproutEmerged(payload);
        break;
      case 'hormonal-trigger':
        handleHormonalTrigger(payload);
        break;
      case 'rhizome-update':
        handleRhizomeUpdate(payload);
        break;
      case 'xylem-transport':
        handleXylemTransport(payload);
        break;
      default:
        appendGenericEvent(payload);
        break;
    }
  }

  function handleStreamToken(payload) {
    var token = extractContent(payload);
    var dataType = payload.data && payload.data.type;

    if (dataType === 'stream.end') {
      var endContent = (payload.data && payload.data.content) || token || '';
      if (endContent) {
        appendXylemText('\n[phloem complete] ' + endContent + '\n');
      }
      els.xylemTerminal.classList.remove('flowing');
      return;
    }

    if (dataType === 'stream.start') {
      clearXylem();
      appendXylemText('[xylem flow initiated]\n');
      els.xylemTerminal.classList.add('flowing');
      ensureSprout(payload.source || 'sprout');
      return;
    }

    if (token) {
      appendXylemText(token);
      els.xylemTerminal.classList.add('flowing');
      setTimeout(function () {
        els.xylemTerminal.classList.remove('flowing');
      }, 400);
    }
  }

  function handleThoughtBranch(payload) {
    var thought = extractContent(payload);
    if (!thought) {
      return;
    }

    clearPlaceholder(els.thoughtStream);

    var entry = document.createElement('div');
    entry.className = 'thought-entry';
    entry.textContent = thought;
    els.thoughtStream.prepend(entry);

    els.mycorrhizal.classList.add('pulse-active');
    setTimeout(function () {
      els.mycorrhizal.classList.remove('pulse-active');
    }, 1200);

    while (els.thoughtStream.children.length > 12) {
      els.thoughtStream.removeChild(els.thoughtStream.lastChild);
    }
  }

  function handleSproutEmerged(payload) {
    var id = payload.source || ('sprout-' + Date.now());
    ensureSprout(id, payload.data);
  }

  function handleHormonalTrigger(payload) {
    document.body.classList.add('danger-flash');
    els.dangerOverlay.classList.add('active');

    clearPlaceholder(els.triggerFeed, '.panel-placeholder');

    var item = document.createElement('li');
    var message = extractContent(payload) || (payload.data && payload.data.message) || 'Growth inhibited';
    item.textContent = '[' + formatTime(payload.timestamp) + '] ' + message;
    els.triggerFeed.prepend(item);

    setTimeout(function () {
      document.body.classList.remove('danger-flash');
      els.dangerOverlay.classList.remove('active');
    }, 600);

    while (els.triggerFeed.children.length > 8) {
      els.triggerFeed.removeChild(els.triggerFeed.lastChild);
    }
  }

  function handleRhizomeUpdate(payload) {
    clearPlaceholder(els.rhizomeFeed, '.panel-placeholder');

    var item = document.createElement('li');
    var detail = (payload.data && (payload.data.summary || payload.data.action)) || payload.source || 'index mutation';
    item.textContent = '[' + formatTime(payload.timestamp) + '] ' + detail;
    els.rhizomeFeed.prepend(item);

    while (els.rhizomeFeed.children.length > 10) {
      els.rhizomeFeed.removeChild(els.rhizomeFeed.lastChild);
    }
  }

  function handleXylemTransport(payload) {
    var content = extractContent(payload) || JSON.stringify(payload.data || {});
    appendXylemText('[xylem] ' + content + '\n');
    els.xylemTerminal.classList.add('flowing');
    setTimeout(function () {
      els.xylemTerminal.classList.remove('flowing');
    }, 800);
  }

  function appendGenericEvent(payload) {
    appendXylemText('[' + payload.type + '] ' + (payload.source || '') + '\n');
  }

  function ensureSprout(id, data) {
    if (!id) {
      return;
    }

    var existing = document.getElementById('sprout-' + id);
    if (existing) {
      existing.classList.add('active');
      return;
    }

    clearPlaceholder(els.sproutGarden);

    var node = document.createElement('div');
    node.className = 'sprout-node active';
    node.id = 'sprout-' + id;

    var icon = document.createElement('span');
    icon.className = 'sprout-icon';
    icon.textContent = '🌱';
    icon.setAttribute('aria-hidden', 'true');

    var label = document.createElement('span');
    label.className = 'sprout-label';
    label.textContent = (data && data.label) || id;

    node.appendChild(icon);
    node.appendChild(label);
    els.sproutGarden.appendChild(node);
  }

  function appendXylemText(text) {
    if (!text) {
      return;
    }
    var span = document.createElement('span');
    span.className = 'xylem-token';
    span.textContent = text;
    els.xylemOutput.appendChild(span);
    els.xylemTerminal.scrollTop = els.xylemTerminal.scrollHeight;
  }

  function clearXylem() {
    els.xylemOutput.textContent = '';
  }

  function extractContent(payload) {
    if (payload.content) {
      return String(payload.content);
    }
    if (payload.data) {
      if (payload.data.token) {
        return String(payload.data.token);
      }
      if (payload.data.thought) {
        return String(payload.data.thought);
      }
      if (payload.data.content) {
        return String(payload.data.content);
      }
    }
    return '';
  }

  function formatTime(timestamp) {
    if (!timestamp) {
      return new Date().toLocaleTimeString();
    }
    try {
      return new Date(timestamp).toLocaleTimeString();
    } catch (e) {
      return String(timestamp);
    }
  }

  connect();
})();