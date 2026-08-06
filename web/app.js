/* lantest — one frontend, two layouts.
 *
 * Everything a measurement depends on happens over the WebSocket protocol in
 * protocol.go. Nothing here calls a Wails binding to measure anything, which is
 * why this file behaves identically in the desktop window (talking to
 * 127.0.0.1) and in a phone browser (talking to a LAN address). The only
 * desktop-only calls are the native save dialog and opening links, both
 * feature-detected and both optional.
 *
 * Statistics are deliberately NOT computed here. Raw sample windows and raw
 * round-trip times go to the server, which owns the single tested
 * implementation of trimming, percentiles and grading. This page displays what
 * comes back.
 */
'use strict';

// ── configuration ───────────────────────────────────────────────────────────

var CFG = window.LANTEST || {};
var PORT = CFG.port || 8080;
var SAMPLE_MS = CFG.sampleMs || 250;
var WARMUP_MS = CFG.warmupMs || 1000;
var MAX_STREAMS = CFG.maxStreams || 8;

var MSG = {
  PING: 0x01, PONG: 0x02,
  DOWN_START: 0x03, DOWN_DATA: 0x04, DOWN_DONE: 0x05,
  UP_START: 0x06, UP_DATA: 0x07, UP_DONE: 0x08,
  RESULT: 0x09, ABORT: 0x0A,
  HELLO: 0x0B, HELLO_ACK: 0x0C, BUSY: 0x0D,
  PROGRESS: 0x0E, OBSERVE: 0x0F, FINAL: 0x10, STORED: 0x11, STOP: 0x12
};

var MODE_AUTO = 'auto';
var MODE_MANUAL = 'manual';
var DIR_BOTH = 'both';
var DIR_DOWN = 'download';
var DIR_UP = 'upload';

var PHASE_MS = 10000;          // per direction
var IDLE_PINGS = 50;           // idle latency round trips
var IDLE_GAP_MS = 20;
var LOADED_PING_MS = 100;      // ping cadence while the link is saturated
var CHUNK = 64 * 1024;
var MAX_BUFFERED = 4 * 1024 * 1024;
var HOST_BREAKPOINT = 900;

// ── tiny DOM helpers ────────────────────────────────────────────────────────

function $(id) { return document.getElementById(id); }
function text(id, v) { var el = $(id); if (el) el.textContent = v; }
function show(el, on) { if (el) el.hidden = !on; }

var app = $('app');

function setState(s) { app.setAttribute('data-state', s); }
function getState() { return app.getAttribute('data-state'); }
function setLayout(l) { app.setAttribute('data-layout', l); }
function getLayout() { return app.getAttribute('data-layout'); }

function setDot(cls) { $('conn-dot').className = 'dot ' + cls; }

function banner(msg, isError) {
  var el = $('banner');
  if (!msg) { show(el, false); return; }
  el.textContent = msg;
  el.className = isError ? 'error' : '';
  show(el, true);
}

function fmtMbps(v) {
  if (!isFinite(v)) return '--';
  if (v >= 100) return v.toFixed(0);
  if (v >= 10) return v.toFixed(1);
  return v.toFixed(2);
}
function fmtMs(v) {
  if (!isFinite(v)) return '--';
  return v >= 10 ? v.toFixed(0) : v.toFixed(1);
}
// Binary divisors with binary labels, matching FormatBytes in stats.go. Frame
// and buffer sizes are powers of two; rendering 65536 as "66 kB" reads as a bug.
function fmtBytes(n) {
  if (n >= (1 << 30)) return (n / (1 << 30)).toFixed(2) + ' GiB';
  if (n >= (1 << 20)) return (n / (1 << 20)).toFixed(1) + ' MiB';
  if (n >= (1 << 10)) return (n / (1 << 10)).toFixed(0) + ' KiB';
  return n + ' B';
}

function encodeJSON(obj) { return new TextEncoder().encode(JSON.stringify(obj)); }
function decodeJSON(bytes) { return JSON.parse(new TextDecoder().decode(bytes)); }

function randomId() {
  var b = new Uint8Array(8);
  (window.crypto || window.msCrypto).getRandomValues(b);
  return Array.prototype.map.call(b, function (x) {
    return ('0' + x.toString(16)).slice(-2);
  }).join('');
}

function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }

// ── transport ───────────────────────────────────────────────────────────────

// wsURL resolves where the server lives. In a browser that is wherever the page
// came from. In the desktop window the page is served from the shell's own
// scheme, so the server is on loopback at the port config.js reported.
function wsURL() {
  if (location.protocol === 'https:') return 'wss://' + location.host + '/ws';
  if (location.protocol === 'http:') return 'ws://' + location.host + '/ws';
  return 'ws://127.0.0.1:' + PORT + '/ws';
}

function isDesktop() {
  return !!(window.go && window.go.main && window.go.main.DesktopApp);
}

function BusyError(message, peer) {
  this.name = 'BusyError';
  this.message = message || 'Serverul este ocupat.';
  this.peer = peer || '';
}
BusyError.prototype = Object.create(Error.prototype);

function sendRaw(conn, type, payload) {
  var ws = conn.ws;
  if (!ws || ws.readyState !== WebSocket.OPEN) return false;
  var buf = new Uint8Array(1 + (payload ? payload.length : 0));
  buf[0] = type;
  if (payload && payload.length) buf.set(payload, 1);
  ws.send(buf);
  return true;
}

function sendJSON(conn, type, obj) { return sendRaw(conn, type, encodeJSON(obj)); }

// openConn dials and completes the HELLO handshake. It resolves once the server
// has accepted the role, and rejects with a BusyError when the single-test lock
// is already held — that refusal is shown verbatim rather than being retried.
function openConn(hello) {
  return new Promise(function (resolve, reject) {
    var ws;
    try {
      ws = new WebSocket(wsURL());
    } catch (e) {
      reject(new Error('Nu pot deschide conexiunea: ' + e.message));
      return;
    }
    ws.binaryType = 'arraybuffer';

    var conn = { ws: ws, handlers: {}, onClose: null, ack: null };
    conn.on = function (type, fn) { this.handlers[type] = fn; return this; };

    var settled = false;
    var timer = setTimeout(function () {
      if (!settled) { settled = true; try { ws.close(); } catch (e) {} reject(new Error('Serverul nu răspunde.')); }
    }, 10000);

    ws.onopen = function () { sendRaw(conn, MSG.HELLO, encodeJSON(hello)); };

    ws.onerror = function () {
      if (!settled) { settled = true; clearTimeout(timer); reject(new Error('Conexiune eșuată.')); }
    };

    ws.onclose = function (ev) {
      if (!settled) {
        settled = true; clearTimeout(timer);
        reject(new Error('Conexiunea s-a închis înainte de confirmare.'));
        return;
      }
      if (conn.onClose) conn.onClose(ev);
    };

    ws.onmessage = function (ev) {
      var d = new Uint8Array(ev.data);
      if (!d.length) return;
      var type = d[0], payload = d.subarray(1);

      if (!settled) {
        settled = true; clearTimeout(timer);
        if (type === MSG.HELLO_ACK) {
          conn.ack = decodeJSON(payload);
          resolve(conn);
        } else if (type === MSG.BUSY) {
          var b = decodeJSON(payload);
          try { ws.close(); } catch (e) {}
          reject(new BusyError(b.message, b.peer));
        } else {
          try { ws.close(); } catch (e) {}
          reject(new Error('Răspuns neașteptat la HELLO.'));
        }
        return;
      }

      var h = conn.handlers[type];
      if (h) h(payload);
    };
  });
}

function closeConn(conn) {
  if (!conn || !conn.ws) return;
  conn.onClose = null;
  try { conn.ws.close(1000); } catch (e) {}
}

// ── live chart ──────────────────────────────────────────────────────────────

function cssVar(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// niceMax rounds an axis up to a readable value with a little headroom. The
// steps are deliberately fine: with only 1/2/5/10 available, a peak of 1086
// lands on 2000 and the plot uses half its height, which is the opposite of
// what someone zooming in to read detail wants.
var NICE_STEPS = [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10];

function niceMax(v) {
  if (!isFinite(v) || v <= 0) return 1;
  v *= 1.08;
  var exp = Math.floor(Math.log10(v));
  var base = Math.pow(10, exp);
  var n = v / base;
  for (var i = 0; i < NICE_STEPS.length; i++) {
    if (n <= NICE_STEPS[i]) return NICE_STEPS[i] * base;
  }
  return 10 * base;
}

var PHASE_LABELS = {
  latency: 'Latență',
  download: 'Download',
  upload: 'Upload'
};

// MAX_PLOT_POINTS bounds how much the chart draws. A manual run can go for
// minutes, and redrawing tens of thousands of points four times a second on a
// phone would cost more than the measurement itself. Buckets keep the peak
// rather than a sample of it, so decimation never hides a spike.
var MAX_PLOT_POINTS = 900;

function decimate(points, limit) {
  if (points.length <= limit) return points;
  var bucket = Math.ceil(points.length / limit);
  var out = [];
  for (var i = 0; i < points.length; i += bucket) {
    var peak = points[i];
    for (var j = i + 1; j < i + bucket && j < points.length; j++) {
      if (points[j].v > peak.v) peak = points[j];
    }
    out.push(peak);
  }
  // Always keep the final point so the line ends where the data does.
  if (out[out.length - 1] !== points[points.length - 1]) {
    out.push(points[points.length - 1]);
  }
  return out;
}

// Layout of the plot area inside the canvas. Shared between drawing and hit
// testing: a click has to land on the same pixel the line was drawn at.
var CHART_PAD = { t: 20, r: 54, b: 26, l: 56 };

// MIN_ZOOM_MS stops a stray click from zooming to a zero-width window.
var MIN_ZOOM_MS = 250;

// sliceView narrows a series to the visible window, keeping one point beyond
// each edge so lines run to the border instead of stopping short of it.
function sliceView(points, from, to) {
  if (!points.length) return points;
  var a = 0, b = points.length - 1;
  while (a < points.length && points[a].t < from) a++;
  while (b >= 0 && points[b].t > to) b--;
  if (a > 0) a--;
  if (b < points.length - 1) b++;
  if (a > b) return [];
  return points.slice(a, b + 1);
}

function nearestPoint(points, tMs) {
  if (!points.length) return null;
  var best = points[0], bestD = Math.abs(points[0].t - tMs);
  for (var i = 1; i < points.length; i++) {
    var d = Math.abs(points[i].t - tMs);
    if (d < bestD) { bestD = d; best = points[i]; }
  }
  return best;
}

function fmtClock(ms) {
  if (ms >= 60000) {
    var total = Math.floor(ms / 1000);
    return Math.floor(total / 60) + ':' + ('0' + (total % 60)).slice(-2);
  }
  return (ms / 1000).toFixed(1) + 's';
}

// Chart draws throughput and latency on one timeline with two axes. Latency is
// overlaid rather than shown separately on purpose: the whole point of the tool
// is watching the latency line climb exactly while the throughput area fills.
//
// It is interactive: drag across it to zoom into a time range, wheel to zoom
// around the pointer, double click (or double tap) to go back to the whole run.
// Zooming is not just visual — the vertical scales and the decimation are both
// recomputed from the visible window, so magnifying a quiet stretch actually
// resolves detail that the full view had averaged away.
function Chart(canvas) {
  this.canvas = canvas;
  this.ctx = canvas.getContext('2d');
  this.w = 0;
  this.h = 0;
  this.view = null;    // {from,to} in ms; null means the whole run
  this.sel = null;     // {x0,x1} in px while a selection drag is in progress
  this.cursor = null;  // px, for the crosshair readout
  this.onViewChange = null;
  this.reset();

  var self = this;
  if (window.ResizeObserver) {
    new ResizeObserver(function () { self.resize(); }).observe(canvas);
  } else {
    window.addEventListener('resize', function () { self.resize(); });
  }
  this.resize();
  this.attach();
}

Chart.prototype.reset = function () {
  this.tp = [];
  this.lat = [];
  this.phases = [];
  this.view = null;
  this.sel = null;
  this.cursor = null;
  if (this.onViewChange) this.onViewChange();
  this.draw();
};

Chart.prototype.resize = function () {
  var dpr = window.devicePixelRatio || 1;
  var rect = this.canvas.getBoundingClientRect();
  if (!rect.width || !rect.height) return;
  this.w = rect.width;
  this.h = rect.height;
  this.canvas.width = Math.round(this.w * dpr);
  this.canvas.height = Math.round(this.h * dpr);
  this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  this.draw();
};

Chart.prototype.startPhase = function (name, tMs) {
  if (this.phases.length) this.phases[this.phases.length - 1].to = tMs;
  this.phases.push({ name: name, from: tMs, to: tMs });
};

Chart.prototype.addThroughput = function (tMs, mbps) {
  this.tp.push({ t: tMs, v: mbps });
  if (this.phases.length) this.phases[this.phases.length - 1].to = tMs;
  this.draw();
};

Chart.prototype.addLatency = function (tMs, ms) {
  this.lat.push({ t: tMs, v: ms });
  if (this.phases.length) this.phases[this.phases.length - 1].to = tMs;
  // Latency arrives ~10x more often than throughput samples; redrawing on each
  // one is wasted work when a throughput sample is about to redraw anyway.
  if (this.lat.length % 4 === 0) this.draw();
};

Chart.prototype.maxT = function () {
  var m = 0;
  for (var i = 0; i < this.tp.length; i++) if (this.tp[i].t > m) m = this.tp[i].t;
  for (var j = 0; j < this.lat.length; j++) if (this.lat[j].t > m) m = this.lat[j].t;
  for (var k = 0; k < this.phases.length; k++) if (this.phases[k].to > m) m = this.phases[k].to;
  return m;
};

Chart.prototype.hasData = function () {
  return this.tp.length > 0 || this.lat.length > 0;
};

// fullRange is the whole run; range is what is currently on screen.
Chart.prototype.fullRange = function () {
  return { from: 0, to: Math.max(this.maxT(), 1000) };
};

Chart.prototype.range = function () {
  return this.view || this.fullRange();
};

Chart.prototype.zoomed = function () { return !!this.view; };

Chart.prototype.plot = function (w, h) {
  w = w === undefined ? this.w : w;
  h = h === undefined ? this.h : h;
  return {
    x: CHART_PAD.l,
    y: CHART_PAD.t,
    w: w - CHART_PAD.l - CHART_PAD.r,
    h: h - CHART_PAD.t - CHART_PAD.b
  };
};

Chart.prototype.timeAt = function (px) {
  var p = this.plot(), r = this.range();
  if (p.w <= 0) return r.from;
  var f = (px - p.x) / p.w;
  return r.from + Math.max(0, Math.min(1, f)) * (r.to - r.from);
};

Chart.prototype.setView = function (from, to) {
  var full = this.fullRange();
  from = Math.max(full.from, Math.min(from, to));
  to = Math.min(full.to, Math.max(from, to));
  if (to - from < MIN_ZOOM_MS) return;
  // A window that covers everything is not a zoom; drop back to follow mode so
  // a live run keeps extending the axis.
  this.view = (from <= full.from && to >= full.to) ? null : { from: from, to: to };
  if (this.onViewChange) this.onViewChange();
  this.draw();
};

Chart.prototype.resetZoom = function () {
  if (!this.view) return;
  this.view = null;
  if (this.onViewChange) this.onViewChange();
  this.draw();
};

Chart.prototype.wheelZoom = function (px, deltaY) {
  var r = this.range();
  var span = r.to - r.from;
  var focus = this.timeAt(px);
  var span2 = span * (deltaY < 0 ? 0.75 : 1.35);
  var full = this.fullRange();

  if (span2 >= full.to - full.from) { this.resetZoom(); return; }
  if (span2 < MIN_ZOOM_MS) span2 = MIN_ZOOM_MS;

  var frac = span > 0 ? (focus - r.from) / span : 0.5;
  this.setView(focus - frac * span2, focus + (1 - frac) * span2);
};

// attach wires pointer, wheel and double-tap handling. Pointer events cover
// mouse, trackpad and touch with one code path.
Chart.prototype.attach = function () {
  var self = this;
  var dragging = false;
  var startX = 0;
  var lastTap = 0;

  function localX(e) {
    return e.clientX - self.canvas.getBoundingClientRect().left;
  }

  function inPlot(x) {
    var p = self.plot();
    return x >= p.x && x <= p.x + p.w;
  }

  this.canvas.addEventListener('pointerdown', function (e) {
    if (e.button !== undefined && e.button !== 0) return;
    if (!self.hasData()) return;
    var x = localX(e);
    if (!inPlot(x)) return;
    dragging = true;
    startX = x;
    self.sel = { x0: x, x1: x };
    try { self.canvas.setPointerCapture(e.pointerId); } catch (err) {}
    e.preventDefault();
  });

  this.canvas.addEventListener('pointermove', function (e) {
    if (!self.hasData()) return;
    var x = localX(e);
    self.cursor = inPlot(x) ? x : null;
    if (dragging) self.sel = { x0: startX, x1: x };
    self.draw();
  });

  this.canvas.addEventListener('pointerup', function (e) {
    var x = localX(e);
    try { self.canvas.releasePointerCapture(e.pointerId); } catch (err) {}

    if (dragging) {
      dragging = false;
      self.sel = null;
      // A drag of a few pixels is a click that wobbled, not a selection.
      if (Math.abs(x - startX) > 6) {
        self.zoomToPixels(startX, x);
        lastTap = 0;
        return;
      }
    }

    // Double tap resets, since dblclick is unreliable on touch.
    var now = Date.now();
    if (now - lastTap < 350) {
      self.resetZoom();
      lastTap = 0;
    } else {
      lastTap = now;
      self.draw();
    }
  });

  this.canvas.addEventListener('pointercancel', function () {
    dragging = false;
    self.sel = null;
    self.draw();
  });

  this.canvas.addEventListener('pointerleave', function () {
    self.cursor = null;
    if (!dragging) self.draw();
  });

  this.canvas.addEventListener('dblclick', function (e) {
    e.preventDefault();
    self.resetZoom();
  });

  this.canvas.addEventListener('wheel', function (e) {
    if (!self.hasData()) return;
    e.preventDefault();
    self.wheelZoom(localX(e), e.deltaY);
  }, { passive: false });
};

Chart.prototype.zoomToPixels = function (x0, x1) {
  var a = this.timeAt(Math.min(x0, x1));
  var b = this.timeAt(Math.max(x0, x1));
  this.setView(a, b);
};

Chart.prototype.draw = function () {
  this.render(this.ctx, this.w, this.h, null, true);
};

// render draws the chart. overlays covers the crosshair and the selection band,
// which belong on screen but not in an exported image.
Chart.prototype.render = function (ctx, w, h, theme, overlays) {
  if (!w || !h) return;

  var t = theme || {
    bg: cssVar('--bg-soft'),
    grid: cssVar('--border'),
    muted: cssVar('--muted'),
    down: cssVar('--down'),
    lat: cssVar('--lat'),
    fg: cssVar('--fg'),
    card: cssVar('--card'),
    border: cssVar('--border')
  };

  var p = this.plot(w, h);
  if (p.w <= 0 || p.h <= 0) return;

  ctx.save();
  ctx.clearRect(0, 0, w, h);
  ctx.fillStyle = t.bg;
  ctx.fillRect(0, 0, w, h);

  var font = '10px -apple-system, system-ui, sans-serif';

  if (!this.hasData()) {
    ctx.fillStyle = t.muted;
    ctx.font = '13px -apple-system, system-ui, sans-serif';
    ctx.textAlign = 'center';
    ctx.fillText('Se așteaptă date…', w / 2, h / 2);
    ctx.restore();
    return;
  }

  var r = this.range();
  var span = Math.max(r.to - r.from, 1);
  var i;

  // Everything below works from the visible window: the vertical scales and the
  // decimation both follow the zoom, which is what makes zooming reveal detail
  // rather than just enlarge the same averaged line.
  var tpAll = sliceView(this.tp, r.from, r.to);
  var latAll = sliceView(this.lat, r.from, r.to);

  var vMax = 0, lMax = 0;
  for (i = 0; i < tpAll.length; i++) if (tpAll[i].v > vMax) vMax = tpAll[i].v;
  for (i = 0; i < latAll.length; i++) if (latAll[i].v > lMax) lMax = latAll[i].v;
  vMax = niceMax(vMax || 1);
  lMax = niceMax(lMax || 10);

  var tp = decimate(tpAll, MAX_PLOT_POINTS);
  var lat = decimate(latAll, MAX_PLOT_POINTS);

  var X = function (ms) { return p.x + ((ms - r.from) / span) * p.w; };
  var Y = function (v) { return p.y + p.h - (v / vMax) * p.h; };
  var L = function (v) { return p.y + p.h - (v / lMax) * p.h; };

  ctx.save();
  ctx.beginPath();
  ctx.rect(p.x, p.y, p.w, p.h);
  ctx.clip();

  // Phase bands, so it is obvious which numbers belong to which direction.
  ctx.font = font;
  ctx.textAlign = 'center';
  for (i = 0; i < this.phases.length; i++) {
    var ph = this.phases[i];
    if (ph.to <= ph.from) continue;
    var x0 = X(ph.from), x1 = X(ph.to);
    if (x1 < p.x || x0 > p.x + p.w) continue;
    ctx.fillStyle = i % 2 ? 'rgba(127,127,127,.05)' : 'rgba(127,127,127,.10)';
    ctx.fillRect(x0, p.y, x1 - x0, p.h);
  }

  // Grid.
  ctx.strokeStyle = t.grid;
  ctx.lineWidth = 0.5;
  for (i = 0; i <= 4; i++) {
    var gy = p.y + (p.h / 4) * i;
    ctx.beginPath();
    ctx.moveTo(p.x, gy);
    ctx.lineTo(p.x + p.w, gy);
    ctx.stroke();
  }

  // Throughput: filled area plus line.
  if (tp.length) {
    ctx.beginPath();
    ctx.moveTo(X(tp[0].t), Y(0));
    for (i = 0; i < tp.length; i++) ctx.lineTo(X(tp[i].t), Y(tp[i].v));
    ctx.lineTo(X(tp[tp.length - 1].t), Y(0));
    ctx.closePath();
    ctx.fillStyle = hexToRGBA(t.down, 0.16);
    ctx.fill();

    ctx.beginPath();
    for (i = 0; i < tp.length; i++) {
      var xx = X(tp[i].t), yy = Y(tp[i].v);
      if (i === 0) ctx.moveTo(xx, yy); else ctx.lineTo(xx, yy);
    }
    ctx.strokeStyle = t.down;
    ctx.lineWidth = 2;
    ctx.lineJoin = 'round';
    ctx.stroke();

    // Once zoomed in far enough that samples are visibly apart, mark them: it
    // makes it obvious where a reading actually is rather than implying the
    // line was measured continuously.
    if (tp.length > 1 && p.w / tp.length > 14) {
      ctx.fillStyle = t.down;
      for (i = 0; i < tp.length; i++) {
        ctx.beginPath();
        ctx.arc(X(tp[i].t), Y(tp[i].v), 2.5, 0, Math.PI * 2);
        ctx.fill();
      }
    }
  }

  // Latency line on the right axis.
  if (lat.length) {
    ctx.beginPath();
    for (i = 0; i < lat.length; i++) {
      var lx = X(lat[i].t), ly = L(lat[i].v);
      if (i === 0) ctx.moveTo(lx, ly); else ctx.lineTo(lx, ly);
    }
    ctx.strokeStyle = t.lat;
    ctx.lineWidth = 1.5;
    ctx.stroke();
  }

  // Selection band while a zoom drag is in progress.
  if (overlays && this.sel) {
    var sx0 = Math.min(this.sel.x0, this.sel.x1);
    var sx1 = Math.max(this.sel.x0, this.sel.x1);
    ctx.fillStyle = hexToRGBA(t.down, 0.18);
    ctx.fillRect(sx0, p.y, sx1 - sx0, p.h);
    ctx.strokeStyle = t.down;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(sx0 + 0.5, p.y); ctx.lineTo(sx0 + 0.5, p.y + p.h);
    ctx.moveTo(sx1 - 0.5, p.y); ctx.lineTo(sx1 - 0.5, p.y + p.h);
    ctx.stroke();
  }

  ctx.restore(); // end clip

  // Phase labels sit above the plot, so they are drawn outside the clip.
  ctx.font = font;
  ctx.textAlign = 'center';
  ctx.fillStyle = t.muted;
  for (i = 0; i < this.phases.length; i++) {
    var ph2 = this.phases[i];
    if (ph2.to <= ph2.from) continue;
    var a = Math.max(X(ph2.from), p.x), b = Math.min(X(ph2.to), p.x + p.w);
    if (b - a > 54) {
      ctx.fillText(PHASE_LABELS[ph2.name] || ph2.name, (a + b) / 2, p.y - 7);
    }
  }

  // Axes.
  ctx.fillStyle = t.muted;
  for (i = 0; i <= 4; i++) {
    var gy2 = p.y + (p.h / 4) * i;
    ctx.textAlign = 'right';
    ctx.fillText(axisLabel(vMax * (4 - i) / 4), p.x - 6, gy2 + 3);
    ctx.textAlign = 'left';
    ctx.fillText(axisLabel(lMax * (4 - i) / 4), p.x + p.w + 6, gy2 + 3);
  }
  ctx.textAlign = 'center';
  for (i = 0; i <= 4; i++) {
    ctx.fillText(fmtClock(r.from + span * i / 4), p.x + (p.w / 4) * i, h - CHART_PAD.b + 15);
  }

  ctx.textAlign = 'left';
  ctx.fillStyle = t.down;
  ctx.fillText('Mbps', p.x - 46, p.y - 7);
  ctx.textAlign = 'right';
  ctx.fillStyle = t.lat;
  ctx.fillText('ms', w - 8, p.y - 7);

  // Crosshair readout.
  if (overlays && this.cursor !== null && !this.sel) {
    this.drawCursor(ctx, p, t, X, Y, L, tpAll, latAll);
  }

  ctx.restore();
};

Chart.prototype.drawCursor = function (ctx, p, t, X, Y, L, tpAll, latAll) {
  var at = this.timeAt(this.cursor);
  var tpPt = nearestPoint(tpAll, at);
  var latPt = nearestPoint(latAll, at);
  if (!tpPt && !latPt) return;

  var cx = this.cursor;
  ctx.save();
  ctx.strokeStyle = t.muted;
  ctx.lineWidth = 1;
  ctx.setLineDash([3, 3]);
  ctx.beginPath();
  ctx.moveTo(cx + 0.5, p.y);
  ctx.lineTo(cx + 0.5, p.y + p.h);
  ctx.stroke();
  ctx.setLineDash([]);

  if (tpPt) {
    ctx.fillStyle = t.down;
    ctx.beginPath();
    ctx.arc(X(tpPt.t), Y(tpPt.v), 3.5, 0, Math.PI * 2);
    ctx.fill();
  }
  if (latPt) {
    ctx.fillStyle = t.lat;
    ctx.beginPath();
    ctx.arc(X(latPt.t), L(latPt.v), 3, 0, Math.PI * 2);
    ctx.fill();
  }

  var lines = [fmtClock(at)];
  if (tpPt) lines.push(fmtMbps(tpPt.v) + ' Mbps');
  if (latPt) lines.push(fmtMs(latPt.v) + ' ms');

  ctx.font = '11px -apple-system, system-ui, sans-serif';
  var wBox = 0;
  for (var i = 0; i < lines.length; i++) {
    wBox = Math.max(wBox, ctx.measureText(lines[i]).width);
  }
  wBox += 16;
  var hBox = 14 * lines.length + 10;
  var bx = cx + 10;
  if (bx + wBox > p.x + p.w) bx = cx - 10 - wBox;
  var by = p.y + 6;

  ctx.fillStyle = t.card || t.bg;
  ctx.strokeStyle = t.border || t.grid;
  ctx.lineWidth = 1;
  ctx.globalAlpha = 0.96;
  ctx.beginPath();
  if (ctx.roundRect) ctx.roundRect(bx, by, wBox, hBox, 6);
  else ctx.rect(bx, by, wBox, hBox);
  ctx.fill();
  ctx.stroke();
  ctx.globalAlpha = 1;

  ctx.textAlign = 'left';
  var ty = by + 16;
  ctx.fillStyle = t.muted;
  ctx.fillText(lines[0], bx + 8, ty);
  if (tpPt) {
    ty += 14;
    ctx.fillStyle = t.down;
    ctx.fillText(lines[1], bx + 8, ty);
  }
  if (latPt) {
    ty += 14;
    ctx.fillStyle = t.lat;
    ctx.fillText(lines[lines.length - 1], bx + 8, ty);
  }
  ctx.restore();
};

function axisLabel(v) {
  if (v >= 100) return v.toFixed(0);
  if (v >= 10) return v % 1 === 0 ? v.toFixed(0) : v.toFixed(1);
  if (v >= 1) return v.toFixed(1);
  return v.toFixed(2);
}

// hexToRGBA accepts the hex colours used in the stylesheet and falls back to a
// neutral tint for anything it cannot parse.
function hexToRGBA(color, alpha) {
  var m = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(color || '');
  if (!m) return 'rgba(96,165,250,' + alpha + ')';
  return 'rgba(' + parseInt(m[1], 16) + ',' + parseInt(m[2], 16) + ',' + parseInt(m[3], 16) + ',' + alpha + ')';
}

var chart = null;

// ── run state ───────────────────────────────────────────────────────────────

var run = null;
var observer = null;
var observerPing = null;
var reconnectTimer = null;
var lastStored = null;
var settings = {
  streams: CFG.defaultStreams || 4,
  mode: MODE_AUTO,
  direction: DIR_BOTH
};

function newRun(streams, mode, direction) {
  return {
    streams: streams,
    mode: mode,
    direction: direction,
    manual: mode === MODE_MANUAL,
    stopping: false,
    elapsedTimer: null,
    sessionId: randomId(),
    control: null,
    streamConns: [],
    startedAt: 0,
    phase: 'idle',
    aborted: false,
    abortReason: '',
    hidden: false,
    caveats: [],
    pendingPings: new Map(),
    pingPhase: new Map(),
    sent: { idle: 0, download: 0, upload: 0 },
    rtt: { idle: [], download: [], upload: [] },
    lag: [],
    lastRTT: NaN,
    down: { start: 0, total: 0, frames: 0, windows: new Map(), serverBytes: 0 },
    up: { start: 0, sent: 0 },
    pinger: null,
    sampler: null,
    lagTimer: null,
    lagLast: 0
  };
}

// bucketName maps a test phase onto the latency bucket it belongs to.
function bucketName(phase) {
  if (phase === 'download') return 'download';
  if (phase === 'upload') return 'upload';
  return 'idle';
}

function addCaveat(msg) {
  if (run && run.caveats.indexOf(msg) === -1) run.caveats.push(msg);
}

// ── latency ─────────────────────────────────────────────────────────────────

function sendPingOn(conn) {
  if (!conn || !conn.ws || conn.ws.readyState !== WebSocket.OPEN) return NaN;
  var t0 = performance.now();
  var buf = new ArrayBuffer(9);
  var v = new DataView(buf);
  v.setUint8(0, MSG.PING);
  v.setFloat64(1, t0);
  conn.ws.send(buf);

  // Tag by the phase the ping was *sent* in, not the one running when the reply
  // turns up. Under load a reply can arrive after the phase boundary, and
  // bucketing it on arrival credits the download's latency to the upload.
  // Counting what was sent also lets the server notice when replies simply
  // never came back during the load at all.
  if (run && conn === run.control) {
    var b = bucketName(run.phase);
    run.pingPhase.set(t0, b);
    run.sent[b] += 1;
  }
  return t0;
}

// pingOnce resolves with the round trip time, or null if the answer never came.
function pingOnce(conn) {
  return new Promise(function (resolve) {
    var t0 = sendPingOn(conn);
    if (!isFinite(t0)) { resolve(null); return; }
    run.pendingPings.set(t0, resolve);
    setTimeout(function () {
      if (run && run.pendingPings.delete(t0)) resolve(null);
    }, 5000);
  });
}

function onPong(payload) {
  if (!run) return;
  var view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength);
  if (payload.byteLength < 8) return;
  var sent = view.getFloat64(0);
  var rtt = performance.now() - sent;

  var name = run.pingPhase.get(sent);
  if (name !== undefined) run.pingPhase.delete(sent);
  else name = bucketName(run.phase);
  run.rtt[name].push(rtt);
  run.lastRTT = rtt;
  text('live-rtt', fmtMs(rtt));
  if (chart && run.startedAt) chart.addLatency(performance.now() - run.startedAt, rtt);

  var resolve = run.pendingPings.get(sent);
  if (resolve) { run.pendingPings.delete(sent); resolve(rtt); }
}

// startPinger keeps measuring latency *while* the link is saturated. It runs on
// the control connection, which carries no bulk data, so a ping is not stuck
// behind megabytes of test traffic in the same send queue.
//
// That is necessary but not sufficient. A browser dispatches every WebSocket
// message on one thread, so on a very fast link the reply to a ping can sit in
// the event queue behind the test data itself. The measured round trip then
// includes time the page spent unable to run at all, which is not network
// latency. The second timer below measures exactly that: how late a plain
// 100 ms interval fires while the transfer is running. The server uses it to
// decide whether the bufferbloat figure describes the link or the device.
function startPinger() {
  stopPinger();
  run.pinger = setInterval(function () {
    if (run && !run.aborted) sendPingOn(run.control);
  }, LOADED_PING_MS);

  run.lagLast = performance.now();
  run.lagTimer = setInterval(function () {
    if (!run) return;
    var now = performance.now();
    run.lag.push(Math.max(0, now - run.lagLast - LOADED_PING_MS));
    run.lagLast = now;
  }, LOADED_PING_MS);
}

function stopPinger() {
  if (!run) return;
  if (run.pinger) { clearInterval(run.pinger); run.pinger = null; }
  if (run.lagTimer) { clearInterval(run.lagTimer); run.lagTimer = null; }
}

// ── phases ──────────────────────────────────────────────────────────────────

function setPhase(name, sub) {
  if (run) run.phase = name;
  var labels = {
    latency: 'Se măsoară latența în repaus',
    download: 'Se măsoară download-ul',
    upload: 'Se măsoară upload-ul',
    done: 'Gata'
  };
  text('phase-label', labels[name] || name);
  text('phase-sub', sub || '');
  if (chart && run) chart.startPhase(name, performance.now() - run.startedAt);
}

function setProgress(frac) {
  $('progress-bar').style.width = Math.max(0, Math.min(1, frac)) * 100 + '%';
}

function relayProgress(phase, elapsed, mbps, fraction) {
  if (!run || !run.control) return;
  sendJSON(run.control, MSG.PROGRESS, {
    phase: phase,
    elapsedMs: Math.round(elapsed),
    mbps: mbps,
    rttMs: isFinite(run.lastRTT) ? run.lastRTT : 0,
    fraction: fraction
  });
}

async function runIdleLatency() {
  setPhase('latency', IDLE_PINGS + ' pachete dus-întors, legătura în repaus');
  for (var i = 0; i < IDLE_PINGS; i++) {
    if (run.aborted) return;
    await pingOnce(run.control);
    setProgress((i + 1) / IDLE_PINGS);
    if (i % 4 === 0) relayProgress('latency', performance.now() - run.startedAt, 0, (i + 1) / IDLE_PINGS);
    await sleep(IDLE_GAP_MS);
  }
}

async function runDownload() {
  setPhase('download', run.streams + (run.streams === 1 ? ' stream' : ' streamuri') + ' în paralel');
  var d = run.down;
  d.start = performance.now();
  d.total = 0;
  d.frames = 0;
  d.windows = new Map();
  d.serverBytes = 0;

  var waits = run.streamConns.map(function (c) {
    return new Promise(function (resolve) { c.downDone = resolve; });
  });

  run.streamConns.forEach(function (c) {
    sendJSON(c, MSG.DOWN_START, {
      durationMs: PHASE_MS,
      chunkBytes: CHUNK,
      manual: run.manual
    });
  });

  startPinger();
  var lastBytes = 0, lastAt = d.start;
  run.sampler = setInterval(function () {
    if (!run) return;
    var now = performance.now();
    var dt = now - lastAt;
    if (dt <= 0) return;
    var mbps = (d.total - lastBytes) * 8 / (dt / 1000) / 1e6;
    lastBytes = d.total; lastAt = now;
    var elapsed = now - d.start;
    text('live-mbps', fmtMbps(mbps));
    if (chart) chart.addThroughput(now - run.startedAt, mbps);
    setProgress(run.manual ? 0 : elapsed / PHASE_MS);
    relayProgress('download', now - run.startedAt, mbps, run.manual ? 0 : elapsed / PHASE_MS);
  }, SAMPLE_MS);

  var results = await Promise.all(waits);
  stopSampler();
  stopPinger();

  for (var i = 0; i < results.length; i++) {
    if (results[i] && results[i].totalBytes) d.serverBytes += results[i].totalBytes;
  }
}

function stopSampler() {
  if (run && run.sampler) { clearInterval(run.sampler); run.sampler = null; }
}

function makeUploadBuffer() {
  // One buffer per stream, filled once. Generating randomness inside the send
  // loop would measure the CSPRNG instead of the link.
  var buf = new Uint8Array(1 + CHUNK);
  buf[0] = MSG.UP_DATA;
  var view = buf.subarray(1);
  var crypto = window.crypto || window.msCrypto;
  // getRandomValues caps at 65536 bytes per call.
  for (var off = 0; off < view.length; off += 65536) {
    crypto.getRandomValues(view.subarray(off, Math.min(off + 65536, view.length)));
  }
  return buf;
}

async function runUpload() {
  setPhase('upload', run.streams + (run.streams === 1 ? ' stream' : ' streamuri') + ' în paralel');
  var u = run.up;
  u.start = performance.now();
  u.sent = 0;

  var resultPromise = new Promise(function (resolve) { run.onResult = resolve; });
  // A manual run has no deadline; it ends when the operator presses stop, which
  // pump() notices through run.stopping.
  var deadline = run.manual ? Infinity : u.start + PHASE_MS;

  run.streamConns.forEach(function (c) {
    sendJSON(c, MSG.UP_START, { durationMs: PHASE_MS, manual: run.manual });
    c.upBuf = makeUploadBuffer();
    c.upFinished = false;
    pump(c, deadline);
  });

  startPinger();
  var lastFlushed = 0, lastAt = u.start;
  run.sampler = setInterval(function () {
    if (!run) return;
    var now = performance.now();
    var dt = now - lastAt;
    if (dt <= 0) return;
    // Bytes handed to send() minus what is still queued in the browser is the
    // closest a client can get to "actually on the wire". The authoritative
    // upload figure comes from the server at the end of the phase.
    var flushed = u.sent - bufferedTotal();
    var mbps = (flushed - lastFlushed) * 8 / (dt / 1000) / 1e6;
    lastFlushed = flushed; lastAt = now;
    var elapsed = now - u.start;
    text('live-mbps', fmtMbps(Math.max(0, mbps)));
    if (chart) chart.addThroughput(now - run.startedAt, Math.max(0, mbps));
    setProgress(run.manual ? 0 : elapsed / PHASE_MS);
    relayProgress('upload', now - run.startedAt, Math.max(0, mbps), run.manual ? 0 : elapsed / PHASE_MS);
  }, SAMPLE_MS);

  // A manual upload can run for as long as the operator leaves it, so the wait
  // for the server's aggregate is bounded from the moment it was asked to stop,
  // not from the moment the phase began.
  var result = run.manual
    ? await resultPromise
    : await withTimeout(resultPromise, PHASE_MS + 20000, 'Serverul nu a trimis rezultatul de upload.');
  stopSampler();
  stopPinger();
  return result;
}

function bufferedTotal() {
  var n = 0;
  for (var i = 0; i < run.streamConns.length; i++) {
    var ws = run.streamConns[i].ws;
    if (ws && ws.readyState === WebSocket.OPEN) n += ws.bufferedAmount;
  }
  return n;
}

// pump keeps a stream busy without letting the browser's send queue grow
// without bound. bufferedAmount is the only backpressure signal a WebSocket
// gives us; ignoring it would buffer hundreds of megabytes in the tab and
// report a throughput the network never delivered.
function pump(conn, deadline) {
  if (!run || run.aborted) return;
  var ws = conn.ws;
  if (!ws || ws.readyState !== WebSocket.OPEN) return;

  if (run.stopping || performance.now() >= deadline) {
    if (!conn.upFinished) {
      conn.upFinished = true;
      sendRaw(conn, MSG.UP_DONE, null);
    }
    return;
  }

  var batchEnd = Math.min(performance.now() + 20, deadline);
  while (performance.now() < batchEnd &&
         ws.bufferedAmount < MAX_BUFFERED &&
         ws.readyState === WebSocket.OPEN) {
    ws.send(conn.upBuf);
    run.up.sent += CHUNK;
  }

  var backedUp = ws.bufferedAmount >= MAX_BUFFERED;
  setTimeout(function () { pump(conn, deadline); }, backedUp ? 8 : 0);
}

function withTimeout(promise, ms, message) {
  return Promise.race([
    promise,
    new Promise(function (_, reject) {
      setTimeout(function () { reject(new Error(message || 'Timp expirat.')); }, ms);
    })
  ]);
}

// buildDownloadSamples turns the per-window byte tally into a contiguous series.
// Gaps become zero-byte windows: a gap is a period where nothing arrived, and
// silently skipping it would inflate the result.
function buildDownloadSamples(windows) {
  if (!windows.size) return [];
  var max = -1;
  windows.forEach(function (_, w) { if (w > max) max = w; });
  var out = [];
  for (var w = 0; w <= max; w++) {
    var bytes = windows.get(w) || 0;
    out.push({
      window: w,
      at_ms: (w + 1) * SAMPLE_MS,
      bytes: bytes,
      mbps: bytes * 8 / (SAMPLE_MS / 1000) / 1e6
    });
  }
  return out;
}

// ── the run ─────────────────────────────────────────────────────────────────

async function startTest() {
  if (run) return;
  banner(null);

  stopObserving();
  observedPhase = '';
  observedPeer = '';
  run = newRun(settings.streams, settings.mode, settings.direction);
  setRunControls(true);
  setState('running');
  setDot('busy');
  chart.reset();
  setProgress(0);
  text('live-mbps', '--');
  text('live-rtt', '--');
  setPhase('latency', 'Se conectează…');

  try {
    run.control = await openConn({
      sessionId: run.sessionId,
      role: 'control',
      streams: run.streams,
      ua: navigator.userAgent
    });
    run.control.on(MSG.PONG, onPong);
    run.control.on(MSG.RESULT, function (p) {
      var r = decodeJSON(p);
      if (run && run.onResult) { var f = run.onResult; run.onResult = null; f(r); }
    });
    run.control.on(MSG.STORED, function (p) {
      var s = decodeJSON(p);
      if (run && run.onStored) { var f = run.onStored; run.onStored = null; f(s); }
    });
    run.control.onClose = function () { abortRun('Conexiunea de control s-a pierdut.'); };

    for (var i = 0; i < run.streams; i++) {
      var sc = await openConn({ sessionId: run.sessionId, role: 'stream', index: i });
      bindStream(sc);
      run.streamConns.push(sc);
    }

    run.startedAt = performance.now();
    startElapsedClock();

    // The idle latency probe runs in every mode: without a baseline there is
    // nothing to compare the loaded latency against.
    await runIdleLatency();
    if (run.aborted) throw new Error(run.abortReason);

    if (run.direction !== DIR_UP) {
      await runDownload();
      if (run.aborted) throw new Error(run.abortReason);
    }

    if (run.direction !== DIR_DOWN) {
      await runUpload();
      if (run.aborted) throw new Error(run.abortReason);
    }

    setPhase('done', '');
    setProgress(1);

    var stored = await sendFinal();
    lastStored = stored;
    renderResult(stored);
    setState('done');
    loadHistory();
  } catch (err) {
    handleRunError(err);
  } finally {
    teardownRun();
    setRunControls(false);
    startObserving();
  }
}

// stopTest ends a manual run. The download stops server-side (its send loop
// polls the stop flag between frames) and the upload stops client-side (pump
// sees run.stopping and sends UP_DONE). Either way the phase unwinds through
// its normal completion path, so the result is a finished run rather than an
// aborted one.
function stopTest() {
  if (!run || run.stopping) return;
  run.stopping = true;
  sendRaw(run.control, MSG.STOP, null);
  text('phase-sub', 'Se oprește…');
  var stop = $('btn-stop');
  stop.disabled = true;
  stop.textContent = 'Se oprește…';
}

// setRunControls reveals the stop button for the duration of a manual run. It
// lives inside the live panel, which is the only panel on screen while a test
// is going.
function setRunControls(running) {
  var manual = running && run && run.manual;
  app.setAttribute('data-running', manual ? 'manual' : (running ? 'auto' : 'no'));
  var stop = $('btn-stop');
  stop.disabled = false;
  stop.textContent = 'Oprește testul';
}

function formatElapsed(ms) {
  var total = Math.floor(ms / 1000);
  var m = Math.floor(total / 60), sec = total % 60;
  return m + ':' + (sec < 10 ? '0' : '') + sec;
}

// startElapsedClock shows how long a manual run has been going, since there is
// no progress bar to fill when the end is up to the operator.
function startElapsedClock() {
  stopElapsedClock();
  if (!run || !run.manual) return;
  run.elapsedTimer = setInterval(function () {
    if (!run || run.stopping) return;
    if (run.phase === 'download' || run.phase === 'upload') {
      text('phase-sub', 'Rulează de ' + formatElapsed(performance.now() - run.startedAt) +
        ' — apasă Oprește când vrei să se termine');
    }
  }, 500);
}

function stopElapsedClock() {
  if (run && run.elapsedTimer) { clearInterval(run.elapsedTimer); run.elapsedTimer = null; }
}

function bindStream(conn) {
  conn.on(MSG.DOWN_DATA, function (payload) {
    if (!run) return;
    var d = run.down;
    d.total += payload.length;
    d.frames += 1;
    var w = Math.floor((performance.now() - d.start) / SAMPLE_MS);
    d.windows.set(w, (d.windows.get(w) || 0) + payload.length);
  });
  conn.on(MSG.DOWN_DONE, function (payload) {
    var stats = decodeJSON(payload);
    if (conn.downDone) { var f = conn.downDone; conn.downDone = null; f(stats); }
  });
  conn.onClose = function () {
    // A stream dropping mid-download would otherwise hang the phase forever.
    if (conn.downDone) { var f = conn.downDone; conn.downDone = null; f(null); }
    if (run && !run.aborted && run.phase !== 'done') {
      addCaveat('Un stream s-a închis înainte de final.');
    }
  };
}

function sendFinal() {
  var storedPromise = new Promise(function (resolve) { run.onStored = resolve; });
  sendJSON(run.control, MSG.FINAL, {
    streams: run.streams,
    mode: run.mode,
    direction: run.direction,
    chunkBytes: CHUNK,
    downloadFrames: run.down.frames,
    durationMs: Math.round(performance.now() - run.startedAt),
    downloadSamples: buildDownloadSamples(run.down.windows),
    downloadTotalBytes: run.down.total,
    serverDownloadBytes: run.down.serverBytes,
    rttIdle: run.rtt.idle,
    rttDownload: run.rtt.download,
    rttUpload: run.rtt.upload,
    pingsIdle: run.sent.idle,
    pingsDownload: run.sent.download,
    pingsUpload: run.sent.upload,
    schedulingLag: run.lag,
    reliable: !run.hidden && !run.aborted,
    aborted: run.aborted,
    caveats: run.caveats,
    ua: navigator.userAgent
  });
  return withTimeout(storedPromise, 15000, 'Serverul nu a confirmat salvarea rezultatului.');
}

function abortRun(reason) {
  if (!run || run.aborted) return;
  run.aborted = true;
  run.abortReason = reason;
  addCaveat(reason);
  stopSampler();
  stopPinger();
  // Unblock anything waiting on a phase so the run can unwind rather than hang.
  run.pendingPings.forEach(function (resolve) { resolve(null); });
  run.pendingPings.clear();
  run.streamConns.forEach(function (c) {
    if (c.downDone) { var f = c.downDone; c.downDone = null; f(null); }
  });
  if (run.onResult) { var r = run.onResult; run.onResult = null; r(null); }
  if (run.onStored) { var s = run.onStored; run.onStored = null; s(null); }
}

function handleRunError(err) {
  if (err instanceof BusyError || err.name === 'BusyError') {
    banner(err.message + (err.peer ? ' (rulează: ' + err.peer + ')' : ''), true);
  } else {
    banner('Testul nu s-a putut încheia: ' + err.message +
           ' Rezultatele parțiale nu sunt raportate ca fiind complete.', true);
  }
  setState('waiting');
  setProgress(0);
}

function teardownRun() {
  if (!run) return;
  stopSampler();
  stopPinger();
  stopElapsedClock();
  run.streamConns.forEach(closeConn);
  closeConn(run.control);
  run = null;
  setDot('online');
}

// ── results ─────────────────────────────────────────────────────────────────

function renderResult(stored) {
  if (!stored || !stored.run) {
    banner('Rularea nu a produs un rezultat complet.', true);
    return;
  }
  var r = stored.run;

  text('verdict', r.verdict || '');
  // A grade is only shown when it means something. When the measurement was
  // dominated by the measuring device, printing a red "F" would be asserting
  // the exact thing the verdict just said it cannot assert.
  var bb = r.bufferbloat || {};
  var grade = $('grade');
  var trusted = bb.trustworthy !== false && bb.grade && bb.grade !== '?';
  grade.textContent = trusted ? bb.grade : '?';
  grade.setAttribute('data-grade', trusted ? bb.grade : '');
  grade.title = trusted ? bb.label || '' : 'Latența sub sarcină nu a putut fi atribuită rețelei';

  text('r-down-p50', fmtMbps(r.download.p50_mbps));
  text('r-down-min', fmtMbps(r.download.min_mbps));
  text('r-down-max', fmtMbps(r.download.max_mbps) + ' Mbps');
  text('r-up-p50', fmtMbps(r.upload.p50_mbps));
  text('r-up-min', fmtMbps(r.upload.min_mbps));
  text('r-up-max', fmtMbps(r.upload.max_mbps) + ' Mbps');

  // A run that only went one way must not show an empty card for the direction
  // it never measured.
  var dir = r.direction || DIR_BOTH;
  show($('down-card-wrap'), dir !== DIR_UP);
  show($('up-card-wrap'), dir !== DIR_DOWN);

  var frames = r.frames || {};
  var totalFrames = (frames.download_count || 0) + (frames.upload_count || 0);
  text('r-frames', fmtCount(totalFrames));
  text('r-frame-size', fmtBytes(frames.frame_bytes || 0) + ' fiecare · ' +
    fmtBytes((frames.download_bytes || 0) + (frames.upload_bytes || 0)) + ' total');
  text('r-duration', formatElapsed(r.duration_ms || 0));
  text('r-mode', (r.mode === MODE_MANUAL ? 'manual' : '10 s fix') + ' · ' + directionLabel(dir));
  text('r-lat-idle', fmtMs(r.latency.idle.p50_ms));
  text('r-lat-jitter', fmtMs(r.latency.idle.jitter_ms) + ' ms');
  text('r-lat-loaded', fmtMs(bb.loaded_p95_ms));
  text('r-bloat-delta', trusted ? '+' + fmtMs(bb.delta_ms) + ' ms' : 'necredibil');

  renderComparison(stored.previous, r);
  renderCaveats(r);
  renderAdvanced(r);
}

function renderComparison(prev, r) {
  var el = $('comparison');
  if (!prev) { show(el, false); return; }

  function delta(now, before, unit, lowerIsBetter) {
    if (!before) return '';
    var pct = (now - before) / before * 100;
    var better = lowerIsBetter ? pct < 0 : pct > 0;
    var cls = Math.abs(pct) < 3 ? '' : (better ? 'better' : 'worse');
    var sign = pct >= 0 ? '+' : '';
    return '<span class="' + cls + '">' + sign + pct.toFixed(0) + '%</span>';
  }

  var when = new Date(prev.timestamp);
  el.innerHTML =
    'Față de ultima rulare pe aceeași rețea (' + when.toLocaleString('ro-RO') + '): ' +
    'download ' + delta(r.download.p50_mbps, prev.download.p50_mbps, 'Mbps', false) + ', ' +
    'upload ' + delta(r.upload.p50_mbps, prev.upload.p50_mbps, 'Mbps', false) + ', ' +
    'latență sub sarcină ' + delta(r.bufferbloat.loaded_p95_ms, prev.bufferbloat.loaded_p95_ms, 'ms', true) + '.';
  show(el, true);
}

function renderCaveats(r) {
  var el = $('caveats');
  var items = (r.caveats || []).slice();
  if (r.aborted) items.unshift('Rulare întreruptă: rezultatele sunt parțiale.');
  if (!r.reliable && !r.aborted) items.unshift('Rulare marcată ca nesigură.');
  if (!items.length) { show(el, false); return; }

  var html = '<strong>De reținut</strong><ul>';
  for (var i = 0; i < items.length; i++) {
    html += '<li>' + escapeHTML(items[i]) + '</li>';
  }
  el.innerHTML = html + '</ul>';
  show(el, true);
}

function renderAdvanced(r) {
  text('a-streams', String(r.streams));
  text('a-down', fmtMbps(r.download.p50_mbps) + ' / ' + fmtMbps(r.download.p95_mbps) + ' / ' + fmtMbps(r.download.max_mbps) + ' Mbps');
  text('a-up', fmtMbps(r.upload.p50_mbps) + ' / ' + fmtMbps(r.upload.p95_mbps) + ' / ' + fmtMbps(r.upload.max_mbps) + ' Mbps');
  text('a-lat-idle', fmtMs(r.latency.idle.min_ms) + ' / ' + fmtMs(r.latency.idle.p50_ms) + ' / ' + fmtMs(r.latency.idle.p95_ms) + ' ms (' + r.latency.idle.count + ' pachete)');
  text('a-lat-down', fmtMs(r.latency.loaded_download.p50_ms) + ' / ' + fmtMs(r.latency.loaded_download.p95_ms) +
    ' ms (' + r.latency.loaded_download.count + '/' + r.latency.loaded_download.sent + ' răspunsuri)');
  text('a-lat-up', fmtMs(r.latency.loaded_upload.p50_ms) + ' / ' + fmtMs(r.latency.loaded_upload.p95_ms) +
    ' ms (' + r.latency.loaded_upload.count + '/' + r.latency.loaded_upload.sent + ' răspunsuri)');
  text('a-mode', (r.mode === MODE_MANUAL ? 'manual, până la Stop' : 'automat, 10 s pe direcție') +
    ' · ' + directionLabel(r.direction || DIR_BOTH));
  text('a-frames', fmtCount((r.frames && r.frames.download_count) || 0) + ' / ' +
    fmtCount((r.frames && r.frames.upload_count) || 0) + ' cadre WebSocket');
  text('a-framesize', fmtBytes((r.frames && r.frames.frame_bytes) || 0) +
    ' încărcătură utilă per cadru (mesaje, nu pachete IP)');
  text('a-bloat', 'nota ' + r.bufferbloat.grade + ' — ' + r.bufferbloat.label + ' (+' + fmtMs(r.bufferbloat.delta_ms) + ' ms)' +
    (r.bufferbloat.trustworthy ? '' : ' — NECREDIBIL, vezi mai jos'));
  text('a-buffer', r.bufferbloat.implied_buffer_bytes
    ? fmtBytes(r.bufferbloat.implied_buffer_bytes) + ' ar fi necesari în rețea pentru această creștere'
    : '–');
  text('a-lag', fmtMs(r.latency.scheduling_lag.p95_ms) + ' ms p95 (' + r.latency.scheduling_lag.count + ' probe)');
  text('a-warmup', r.warmup_discarded_ms + ' ms din fiecare direcție');
  text('a-warmup-inline', String(r.warmup_discarded_ms));
  text('a-bytes', fmtBytes(r.download.total_bytes) + ' download / ' + fmtBytes(r.upload.total_bytes) + ' upload (doar ferestrele măsurate)');
  text('a-delta', r.server_download
    ? r.download_delta_pct.toFixed(1) + '% (server: ' + fmtBytes(r.server_download.total_bytes) + ' trimiși)'
    : 'indisponibil');
  text('a-loss', r.packet_loss);
  text('a-net', (r.ssid ? r.ssid + ' · ' : '') + (r.subnet || '–'));
  text('a-client', r.client + (r.client_addr ? ' (' + r.client_addr + ')' : ''));
  text('a-time', new Date(r.timestamp).toLocaleString('ro-RO'));
}

function directionLabel(dir) {
  if (dir === DIR_DOWN) return 'doar download';
  if (dir === DIR_UP) return 'doar upload';
  return 'download și upload';
}

function fmtCount(n) {
  return String(Math.round(n)).replace(/\B(?=(\d{3})+(?!\d))/g, '\u202f');
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, function (c) {
    return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
  });
}

// ── observing ───────────────────────────────────────────────────────────────

async function startObserving() {
  if (observer || run) return;
  try {
    observer = await openConn({ role: 'observer', ua: navigator.userAgent });
    setDot('online');
    observer.on(MSG.OBSERVE, function (p) { handleObserve(decodeJSON(p)); });
    observer.on(MSG.PONG, function () {});
    observer.onClose = function () {
      observer = null;
      setDot('offline');
      scheduleReconnect();
    };
    observerPing = setInterval(function () {
      if (observer) sendPingOn(observer);
    }, 20000);
  } catch (err) {
    setDot('offline');
    scheduleReconnect();
  }
}

function stopObserving() {
  if (observerPing) { clearInterval(observerPing); observerPing = null; }
  if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
  closeConn(observer);
  observer = null;
}

function scheduleReconnect() {
  if (reconnectTimer || run) return;
  reconnectTimer = setTimeout(function () {
    reconnectTimer = null;
    startObserving();
  }, 2000);
}

// handleObserve drives this screen from someone else's test. It is what makes
// the desktop window show the same live chart as the phone in hand.
function handleObserve(ev) {
  if (run) return; // our own test is authoritative for this screen

  if (ev.type === 'peers') {
    updatePeerStatus(ev.peers || [], null);
    return;
  }

  if (ev.type === 'state') {
    if (ev.state === 'running') {
      updatePeerStatus(null, ev.peer);
      observedPeer = ev.peer ? ev.peer.label : '';
      if (getState() !== 'running') {
        setState('running');
        chart.reset();
        setProgress(0);
        text('live-mbps', '--');
        text('live-rtt', '--');
      }
      observedPhase = '';
      setPhaseFromObserver('latency');
      showObservedPeer();
    } else {
      updatePeerStatus(null, null);
      observedPeer = '';
      if (getState() === 'running') setState('waiting');
    }
    return;
  }

  if (ev.type === 'progress' && ev.progress) {
    var p = ev.progress;
    if (ev.peer && ev.peer.label) observedPeer = ev.peer.label;
    if (getState() !== 'running') {
      setState('running');
      observedPhase = '';
    }
    setPhaseFromObserver(p.phase);
    if (p.mbps > 0) {
      text('live-mbps', fmtMbps(p.mbps));
      chart.addThroughput(p.elapsedMs, p.mbps);
    }
    if (p.rttMs > 0) {
      text('live-rtt', fmtMs(p.rttMs));
      chart.addLatency(p.elapsedMs, p.rttMs);
    }
    setProgress(p.fraction);
    return;
  }

  if (ev.type === 'final' && ev.stored) {
    lastStored = ev.stored;
    observedPhase = 'done';
    text('phase-label', 'Gata');
    text('phase-sub', observedPeer ? 'Rulat pe: ' + observedPeer : '');
    renderResult(ev.stored);
    setState('done');
    setProgress(1);
    loadHistory();
  }
}

var observedPhase = '';
var observedPeer = '';

// While another device is testing, this screen mirrors it rather than offering
// a start button that would only be refused. The label says whose run it is, so
// nobody mistakes someone else's numbers for their own.
function setPhaseFromObserver(phase) {
  if (phase === observedPhase) return;
  observedPhase = phase;
  var labels = {
    latency: 'Se măsoară latența în repaus',
    download: 'Se măsoară download-ul',
    upload: 'Se măsoară upload-ul',
    done: 'Gata'
  };
  text('phase-label', labels[phase] || phase);
  showObservedPeer();
  if (chart) chart.startPhase(phase, chart.maxT());
}

// showObservedPeer keeps the "this is not your run" note on screen no matter
// which event arrived first.
function showObservedPeer() {
  text('phase-sub', observedPeer
    ? 'Rulează pe alt dispozitiv: ' + observedPeer
    : 'Rulează pe alt dispozitiv');
}

function updatePeerStatus(peers, activePeer) {
  var box = $('peer-status');
  if (!box) return;
  if (activePeer) {
    box.className = 'peer connected';
    text('peer-text', 'Testează: ' + activePeer.label);
    return;
  }
  if (peers && peers.length) {
    box.className = 'peer connected';
    text('peer-text', 'Conectat: ' + peers.map(function (p) { return p.label; }).join(', ') +
      ' — apasă Pornește pe telefon');
    return;
  }
  box.className = 'peer waiting';
  text('peer-text', 'Aștept telefonul…');
}

// ── history ─────────────────────────────────────────────────────────────────

async function loadHistory() {
  try {
    var resp = await fetch('api/history', { cache: 'no-store' });
    if (!resp.ok) return;
    var body = await resp.json();
    renderHistory(body.runs || [], body.network || {});
  } catch (e) {
    // History is a convenience; failing to read it must not break the page.
  }
}

function renderHistory(runs, network) {
  var el = $('history-list');
  var here = runs.filter(function (r) { return !network.key || r.network_key === network.key; });
  if (!here.length) {
    el.innerHTML = '<p class="history-empty">Nicio rulare pe această rețea încă.</p>';
    return;
  }
  var html = '';
  for (var i = 0; i < Math.min(here.length, 8); i++) {
    var r = here[i];
    // A direction that was never measured shows a dash, not a zero: "0.00↑"
    // reads as "your upload is broken" rather than "we did not test it".
    var dir = r.direction || DIR_BOTH;
    var down = dir === DIR_UP ? '–' : fmtMbps(r.download.p50_mbps);
    var up = dir === DIR_DOWN ? '–' : fmtMbps(r.upload.p50_mbps);
    var grade = (r.bufferbloat && r.bufferbloat.trustworthy === false)
      ? '?'
      : ((r.bufferbloat && r.bufferbloat.grade) || '–');
    html += '<div class="run' + (r.aborted ? ' aborted' : '') + '"' +
      ' title="' + escapeHTML((r.mode === MODE_MANUAL ? 'manual' : '10 s fix') + ' · ' + directionLabel(dir)) + '">' +
      '<span class="when">' + escapeHTML(new Date(r.timestamp).toLocaleString('ro-RO')) +
      (r.mode === MODE_MANUAL ? ' <span class="tag">manual</span>' : '') + '</span>' +
      '<span class="d">' + down + '↓</span>' +
      '<span class="u">' + up + '↑</span>' +
      '<span class="g">' + escapeHTML(grade) + '</span>' +
      '</div>';
  }
  el.innerHTML = html;
}

// ── export ──────────────────────────────────────────────────────────────────

function timestampName(ext) {
  var t = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
  return 'lantest-' + t + '.' + ext;
}

function blobToBase64(blob) {
  return new Promise(function (resolve, reject) {
    var fr = new FileReader();
    fr.onload = function () { resolve(String(fr.result).split(',')[1] || ''); };
    fr.onerror = function () { reject(new Error('Nu pot citi fișierul.')); };
    fr.readAsDataURL(blob);
  });
}

async function saveFile(name, blob) {
  if (isDesktop()) {
    try {
      var b64 = await blobToBase64(blob);
      var path = await window.go.main.DesktopApp.SaveFile(name, b64);
      if (path) banner('Salvat în ' + path, false);
      return;
    } catch (e) {
      // Fall through to the browser download path.
    }
  }
  var url = URL.createObjectURL(blob);
  var a = document.createElement('a');
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(function () { URL.revokeObjectURL(url); }, 10000);
}

function exportJSON() {
  if (!lastStored) return;
  var payload = {
    schema_version: lastStored.run.schema_version,
    exported_at: new Date().toISOString(),
    run: lastStored.run,
    previous_on_this_network: lastStored.previous || null,
    method: {
      warmup_discarded_ms: lastStored.run.warmup_discarded_ms,
      sample_window_ms: SAMPLE_MS,
      download_measured_by: 'client',
      upload_measured_by: 'server',
      packet_loss: lastStored.run.packet_loss
    }
  };
  var blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' });
  saveFile(timestampName('json'), blob);
}

function exportPNG() {
  if (!lastStored) return;
  var r = lastStored.run;
  var W = 1100, H = 720;
  var out = document.createElement('canvas');
  out.width = W;
  out.height = H;
  var ctx = out.getContext('2d');

  // Always export on a light background: a screenshot ends up in a document or
  // a chat, not in a dark-mode browser.
  var theme = { bg: '#f4f5f7', grid: '#e2e5ea', muted: '#6b7280', down: '#2563eb', lat: '#d97706' };
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, W, H);

  ctx.fillStyle = '#14161a';
  ctx.font = '600 26px -apple-system, system-ui, sans-serif';
  ctx.fillText('lantest — ' + (r.ssid || r.subnet || 'rețea locală'), 44, 56);

  ctx.fillStyle = '#6b7280';
  ctx.font = '14px -apple-system, system-ui, sans-serif';
  ctx.fillText(new Date(r.timestamp).toLocaleString('ro-RO') + ' · ' + r.client + ' · ' +
    r.streams + (r.streams === 1 ? ' stream' : ' streamuri'), 44, 80);

  ctx.fillStyle = '#14161a';
  ctx.font = '18px -apple-system, system-ui, sans-serif';
  wrapText(ctx, r.verdict || '', 44, 118, W - 88, 26);

  // Same rule as the result cards: a direction that was never measured gets no
  // box, rather than a box reading 0.00 Mbps.
  var dir = r.direction || DIR_BOTH;
  var boxes = [];
  if (dir !== DIR_UP) {
    boxes.push(['Download p50', fmtMbps(r.download.p50_mbps) + ' Mbps', '#2563eb']);
    boxes.push(['Download min / max',
      fmtMbps(r.download.min_mbps) + ' / ' + fmtMbps(r.download.max_mbps), '#2563eb']);
  }
  if (dir !== DIR_DOWN) {
    boxes.push(['Upload p50', fmtMbps(r.upload.p50_mbps) + ' Mbps', '#059669']);
    boxes.push(['Upload min / max',
      fmtMbps(r.upload.min_mbps) + ' / ' + fmtMbps(r.upload.max_mbps), '#059669']);
  }
  boxes.push(['Latență repaus', fmtMs(r.latency.idle.p50_ms) + ' ms', '#d97706']);
  boxes.push(['Sub sarcină p95', fmtMs(r.bufferbloat.loaded_p95_ms) + ' ms', '#d97706']);

  var bw = (W - 88 - 10 * (boxes.length - 1)) / boxes.length;
  for (var i = 0; i < boxes.length; i++) {
    var x = 44 + i * (bw + 10);
    ctx.fillStyle = '#f4f5f7';
    ctx.fillRect(x, 178, bw, 78);
    ctx.fillStyle = '#6b7280';
    ctx.font = '12px -apple-system, system-ui, sans-serif';
    ctx.fillText(boxes[i][0], x + 14, 202);
    ctx.fillStyle = boxes[i][2];
    ctx.font = '600 ' + (boxes.length > 4 ? 20 : 26) + 'px -apple-system, system-ui, sans-serif';
    ctx.fillText(boxes[i][1], x + 14, 236);
  }

  ctx.save();
  ctx.translate(44, 280);
  chart.render(ctx, W - 88, 340, theme, false);
  ctx.restore();

  ctx.fillStyle = '#6b7280';
  ctx.font = '12px -apple-system, system-ui, sans-serif';
  var scope = chart.zoomed()
    ? 'Grafic mărit: ' + fmtClock(chart.range().from) + ' – ' + fmtClock(chart.range().to) +
      ' din rulare. Cifrele de mai sus acoperă toată rularea.'
    : '';
  ctx.fillText('Bufferbloat: nota ' + r.bufferbloat.grade + ' — ' + r.bufferbloat.label +
    ' (+' + fmtMs(r.bufferbloat.delta_ms) + ' ms sub sarcină)' + (scope ? '   ·   ' + scope : ''), 44, 654);
  ctx.fillText('Primele ' + r.warmup_discarded_ms + ' ms din fiecare direcție sunt eliminate (TCP slow-start). ' +
    'Pierdere de pachete: ' + r.packet_loss + '.', 44, 676);

  out.toBlob(function (blob) {
    if (blob) saveFile(timestampName('png'), blob);
  }, 'image/png');
}

function wrapText(ctx, str, x, y, maxWidth, lineHeight) {
  var words = String(str).split(' ');
  var line = '';
  for (var i = 0; i < words.length; i++) {
    var test = line ? line + ' ' + words[i] : words[i];
    if (ctx.measureText(test).width > maxWidth && line) {
      ctx.fillText(line, x, y);
      y += lineHeight;
      line = words[i];
    } else {
      line = test;
    }
  }
  if (line) ctx.fillText(line, x, y);
}

// ── layout, settings, wiring ────────────────────────────────────────────────

function preferredLayout() {
  var override = null;
  try {
    var params = new URLSearchParams(location.search);
    override = params.get('layout') || localStorage.getItem('lantest.layout');
  } catch (e) {}
  if (override === 'host' || override === 'client') return override;
  return window.innerWidth >= HOST_BREAKPOINT ? 'host' : 'client';
}

function applyLayout(l) {
  setLayout(l);
  $('layout-toggle').textContent = l === 'host' ? 'Vedere telefon' : 'Vedere desktop';
  if (chart) chart.resize();
}

function applyStreams(n) {
  settings.streams = n;
  store('lantest.streams', String(n));
  pressOne('streams', 'data-streams', String(n));
}

function applyMode(mode) {
  settings.mode = mode === MODE_MANUAL ? MODE_MANUAL : MODE_AUTO;
  store('lantest.mode', settings.mode);
  pressOne('mode', 'data-mode', settings.mode);

  // "Both directions" needs two stops to end, which is a confusing control.
  // A manual run is therefore single-direction; pick one if none was chosen.
  var bothBtn = $('direction').querySelector('button[data-direction="both"]');
  var manual = settings.mode === MODE_MANUAL;
  bothBtn.disabled = manual;
  bothBtn.title = manual ? 'O rulare manuală merge într-o singură direcție' : '';
  if (manual && settings.direction === DIR_BOTH) applyDirection(DIR_DOWN);

  text('settings-hint', manual
    ? 'Rularea manuală merge într-o singură direcție, până apeși Oprește. Nu se compară cu rulările de 10 s.'
    : 'Un singur stream TCP rareori saturează Wi-Fi-ul modern. Rulările de 10 s sunt cele comparabile în istoric.');
}

function applyDirection(dir) {
  settings.direction = dir;
  store('lantest.direction', dir);
  pressOne('direction', 'data-direction', dir);
}

function pressOne(groupId, attr, value) {
  var buttons = $(groupId).querySelectorAll('button');
  for (var i = 0; i < buttons.length; i++) {
    buttons[i].setAttribute('aria-pressed',
      buttons[i].getAttribute(attr) === value ? 'true' : 'false');
  }
}

function store(key, value) {
  try { localStorage.setItem(key, value); } catch (e) {}
}

function load(key) {
  try { return localStorage.getItem(key); } catch (e) { return null; }
}

async function loadURLs() {
  try {
    var resp = await fetch('api/urls', { cache: 'no-store' });
    if (!resp.ok) return;
    var body = await resp.json();
    var list = $('url-list');
    list.innerHTML = '';
    (body.urls || []).forEach(function (u) {
      var li = document.createElement('li');
      var a = document.createElement('a');
      a.href = u;
      a.textContent = u;
      a.target = '_blank';
      a.rel = 'noreferrer';
      if (isDesktop()) {
        a.addEventListener('click', function (e) {
          e.preventDefault();
          window.go.main.DesktopApp.OpenURL(u);
        });
      }
      li.appendChild(a);
      list.appendChild(li);
    });
    if (body.urls && body.urls.length) {
      $('qr').src = 'qr.png?t=' + Date.now();
    }
  } catch (e) {}
}

// updateZoomUI keeps the hint honest about what is on screen. The distinction
// matters: zooming changes the picture, never the reported numbers, which are
// always computed over the whole run.
function updateZoomUI() {
  var zoomed = chart.zoomed();
  var btn = $('btn-zoom-reset');
  if (btn) btn.hidden = !zoomed;
  var hint = $('chart-hint');
  if (!hint) return;
  if (zoomed) {
    var r = chart.range();
    hint.textContent = 'Mărit pe ' + fmtClock(r.from) + ' – ' + fmtClock(r.to) +
      ' · dublu-clic pentru tot intervalul · cifrele din carduri rămân pe toată rularea';
  } else {
    hint.textContent = 'Trage peste grafic ca să mărești un interval · rotița mărește · dublu-clic revine';
  }
}

function init() {
  chart = new Chart($('chart'));
  chart.onViewChange = updateZoomUI;
  updateZoomUI();
  $('btn-zoom-reset').addEventListener('click', function () { chart.resetZoom(); });

  applyLayout(preferredLayout());
  var savedStreams = parseInt(load('lantest.streams'), 10);
  applyStreams(savedStreams > 0 && savedStreams <= MAX_STREAMS ? savedStreams : (CFG.defaultStreams || 4));
  applyDirection(load('lantest.direction') || DIR_BOTH);
  applyMode(load('lantest.mode') || MODE_AUTO);

  var net = (CFG.network || {});
  text('net-label', net.ssid || net.subnet || '');

  $('layout-toggle').addEventListener('click', function () {
    var next = getLayout() === 'host' ? 'client' : 'host';
    try { localStorage.setItem('lantest.layout', next); } catch (e) {}
    applyLayout(next);
  });

  $('streams').addEventListener('click', function (e) {
    var b = e.target.closest('button[data-streams]');
    if (b) applyStreams(parseInt(b.getAttribute('data-streams'), 10));
  });

  $('direction').addEventListener('click', function (e) {
    var b = e.target.closest('button[data-direction]');
    if (b && !b.disabled) applyDirection(b.getAttribute('data-direction'));
  });

  $('mode').addEventListener('click', function (e) {
    var b = e.target.closest('button[data-mode]');
    if (b) applyMode(b.getAttribute('data-mode'));
  });

  $('btn-start').addEventListener('click', startTest);
  $('btn-start-local').addEventListener('click', startTest);
  $('btn-stop').addEventListener('click', stopTest);
  $('btn-again').addEventListener('click', function () {
    setState('waiting');
    banner(null);
  });
  $('btn-json').addEventListener('click', exportJSON);
  $('btn-png').addEventListener('click', exportPNG);

  // A locked screen or a backgrounded tab throttles timers and stalls the
  // socket. That does not invalidate the connection, but it does invalidate the
  // numbers, so the run is marked rather than quietly reported as normal.
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState !== 'visible' && run && !run.aborted) {
      run.hidden = true;
      addCaveat('Ecranul a fost stins sau aplicația a trecut în fundal în timpul testului.');
      banner('Ecranul a fost stins în timpul testului — rezultatul este marcat ca nesigur.', false);
    }
  });

  window.addEventListener('beforeunload', function (e) {
    if (run) { e.preventDefault(); e.returnValue = ''; }
  });

  loadURLs();
  loadHistory();
  startObserving();
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', init);
} else {
  init();
}
