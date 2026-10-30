/*! dsh-pet.standalone.js —— 自动构建，勿手改；源: dsh-pet.js + build-standalone.js */
/*!
 * dsh-pet.js —— 网页版看板娘（移植自 PC2005-cloud/dsh-pet，MIT License）
 *
 * 保留功能：
 *   待机呼吸 / 97 个随机动作无缝衔接 / 左右转向 / 屏幕漫游 /
 *   点击 Q 弹 / 拖拽甩抛反弹 / 右键菜单点播动作 / 头顶联想气泡
 * 剔除功能：余额、AI 碎碎念生成、对话、系统通知、桌面模式、多开、设置页、DSH 宿主依赖
 *
 * 用法（一行接入）：
 *   <script src="dsh-pet.js"></script>
 *   <script>DSHPet.init({ assetRoot: '.' })</script>
 *
 * API：
 *   DSHPet.init(options)          初始化（自动拉取 config.jsonc）
 *   DSHPet.play(name)             点播动画（once，播完自动回随机链）
 *   DSHPet.say(text, ms?)         头顶气泡显示一句话（默认 10s 消失）
 *   DSHPet.destroy()              销毁（移除 DOM 与全部定时器）
 *
 * options：
 *   assetRoot   素材根目录（默认 'dsh-pet'，即 ./dsh-pet/assets/...）
 *   configUrl   配置地址（默认 assetRoot + '/assets/config.jsonc'）
 *   size        宽度 px（默认 462，高度 = size × 9/16）
 *   corner      初始角落 top-left/top-right/bottom-left/bottom-right（默认 top-right）
 *   marginX/Y   角落边距 px（默认 24 / 100）
 *   phrases     联想气泡文案池（字符串数组）；非空时按 whisperIntervalSec 周期自动冒泡
 *   whisperIntervalSec  联想气泡周期秒数（默认 300）
 *   zIndex      根节点 z-index（默认 9999）
 *   minViewport 小屏隐藏阈值 {width,height}（默认 {width:768,height:600}，视口小于该值时看板娘隐藏且暂停活动；传 0 禁用）
 *   awaitLoad   等待宿主页面完全加载（window load 事件）后再拉取素材初始化（默认 false；外部网站接入建议传 true，避免抢占宿主首屏带宽）
 */
(function (global) {
  'use strict';

  /* ==================== 常量（原 shared/constants.ts） ==================== */
  var CANVAS_H = 360;
  var FEET_Y = 330;
  var HIT_BOX = { x0: 200, y0: 50, x1: 440, y1: 335 };
  var DRAG_THRESHOLD = 5;
  var PET_REF_WIDTH = 462;
  var THUMB_EXT = '.webm';
  var BUBBLE_DURATION_MS = 10 * 1000;

  /* ==================== 物理参数（原 shared/physics.ts） ==================== */
  var SPRING_K = 200, SPRING_C = 30;
  var TRAIL_KEEP_MS = 200, RELEASE_WINDOW_MS = 150, RELEASE_STALE_MS = 150;
  var MIN_SPAN_MS = 20, SEG_MIN_DT_MS = 8, DEAD_ZONE_SPEED = 500;
  var MAX_THROW_SPEED = 3600, PEAK_WEIGHT = 0.5, ACCEL_REF = 8000, ACCEL_GAIN_MAX = 0.6;
  var GRAVITY = 1400, RESTITUTION = 0.78, GROUND_FRICTION = 2.5;
  var REST_VY = 40, REST_VX = 15, MAX_STEP_DT = 0.05;
  var SQ_SQUASH = 0.55, SQ_DURATION_MS = 220, SQ_SOFT_SPEED = 300, SQ_HARD_SPEED = 1500, SQ_MAX_SQUASH = 0.55;

  /* ==================== 小屏阈值（原 shared/... 无，新增） ==================== */
  function normalizeMinViewport(v) {
    // 默认：视口宽 < 768 或高 < 600 视为移动端/小屏，隐藏看板娘
    var def = { width: 768, height: 600 };
    if (v === undefined || v === null) return def;
    if (v === 0 || v === 'off' || v === false) return null; // 显式禁用
    if (typeof v === 'number') return { width: v, height: 0 };
    var w = Number(v.width), h = Number(v.height);
    return {
      width: Number.isFinite(w) && w > 0 ? w : 0,
      height: Number.isFinite(h) && h > 0 ? h : 0
    };
  }

  /* ==================== 样式注入 ==================== */
  var injectedCss = false;
  function injectCss(assetRoot) {
    if (injectedCss) return;
    injectedCss = true;
    var css = [
      '@font-face{font-family:"ShangshouSoftCandy";src:url("' + assetRoot + '/assets/fonts/上首软糖体.ttf") format("truetype");font-display:swap;font-weight:400}',
      '.dsh-pet-root{position:fixed;z-index:var(--dsh-pet-z,9999);pointer-events:none;user-select:none}',
      '.dsh-pet-root[data-corner="bottom-right"]{right:var(--dsh-pet-mx,24px);bottom:var(--dsh-pet-my,0)}',
      '.dsh-pet-root[data-corner="bottom-left"]{left:var(--dsh-pet-mx,24px);bottom:var(--dsh-pet-my,0)}',
      '.dsh-pet-root[data-corner="top-right"]{right:var(--dsh-pet-mx,24px);top:var(--dsh-pet-my,0)}',
      '.dsh-pet-root[data-corner="top-left"]{left:var(--dsh-pet-mx,24px);top:var(--dsh-pet-my,0)}',
      '.dsh-pet-stage{position:relative;width:var(--dsh-pet-size,462px);height:calc(var(--dsh-pet-size,462px)*9/16);pointer-events:none}',
      '.dsh-pet-video{position:absolute;inset:0;width:100%;height:100%;object-fit:contain;pointer-events:none;opacity:0;transition:opacity .18s ease;transform-origin:center}',
      '.dsh-pet-video.is-front{opacity:1}',
      '.dsh-pet-hit{position:absolute;pointer-events:auto;cursor:url("' + assetRoot + '/assets/pic/cursor-grab.png") 16 16, grab;z-index:1}',
      '.dsh-pet-hit.dragging{cursor:url("' + assetRoot + '/assets/pic/cursor-grabbing.png") 16 16, grabbing}',
      '@media (prefers-reduced-motion: reduce){.dsh-pet-video{transition:none}}',
      '.dsh-pet-root.dsh-pet-hidden{display:none !important}',
      '@media (max-width:768px),(max-height:600px){.dsh-pet-root.dsh-pet-auto-hide{display:none !important}}',
      // 气泡
      '.dsh-pet-bubble{position:absolute;left:50%;transform:translateX(-50%);' +
        'bottom:calc(100% - var(--dsh-pet-size)*0.108);' +
        'min-width:calc(var(--dsh-pet-size)*0.10);max-width:calc(var(--dsh-pet-size)*0.5);' +
        'padding:calc(var(--dsh-pet-size)*0.022) calc(var(--dsh-pet-size)*0.030);' +
        'border-radius:calc(var(--dsh-pet-size)*0.035);background:rgba(255,255,255,.92);' +
        'color:#2b2b2b;font-family:"ShangshouSoftCandy","Yuanti SC","YouYuan","幼圆","Comic Sans MS","PingFang SC","Microsoft YaHei",sans-serif;' +
        'font-size:calc(var(--dsh-pet-size)*0.034);line-height:1.6;z-index:3;pointer-events:none;' +
        'box-shadow:0 calc(var(--dsh-pet-size)*0.009) calc(var(--dsh-pet-size)*0.035) rgba(0,0,0,.14),0 1px 3px rgba(0,0,0,.08);' +
        'backdrop-filter:blur(6px);opacity:0;transition:opacity .25s ease;white-space:normal;overflow-wrap:anywhere}',
      '.dsh-pet-bubble::after{content:"";position:absolute;left:50%;bottom:calc(var(--dsh-pet-size)*-0.017);' +
        'transform:translateX(-50%);border:calc(var(--dsh-pet-size)*0.017) solid transparent;' +
        'border-top-color:rgba(255,255,255,.92);border-bottom:none}',
      '.dsh-pet-bubble.is-on{opacity:1}',
      // 右键菜单
      '.dsh-pet-menu{position:fixed;left:0;top:0;z-index:2147483000;color:#2b2b2b;font-size:13px;line-height:1.5;' +
        "font-family:'Microsoft YaHei UI','Segoe UI','PingFang SC',sans-serif;user-select:none;pointer-events:auto}",
      '.dsh-pet-menu,.dsh-pet-menu *{box-sizing:border-box}',
      '.dsh-pet-menu-column{position:absolute;min-width:150px;max-width:240px;padding:4px;' +
        'background:rgba(255,255,255,.98);border:1px solid rgba(0,0,0,.12);border-radius:8px;' +
        'box-shadow:0 8px 28px rgba(0,0,0,.2);max-height:min(62vh,460px);overflow-y:auto}',
      '.dsh-pet-menu-item{position:relative;display:flex;align-items:center;justify-content:space-between;' +
        'gap:14px;padding:5px 12px;border-radius:6px;white-space:nowrap;cursor:default}',
      '.dsh-pet-menu-item:hover{background:rgba(43,99,255,.14)}',
      '.dsh-pet-menu-item>span:first-child{min-width:0;overflow:hidden;text-overflow:ellipsis}',
      '.dsh-pet-menu-arrow{color:#9aa0a6;font-size:12px;flex:none}'
    ].join('\n');
    var tag = document.createElement('style');
    tag.dataset.dshPet = 'css';
    tag.textContent = css;
    document.head.appendChild(tag);
  }

  /* ==================== 纯逻辑：选择（原 shared/pickers.ts） ==================== */
  function pick(pool, exclude) {
    var entries = exclude !== undefined ? pool.filter(function (n) { return n !== exclude; }) : pool;
    var src = entries.length ? entries : pool;
    return src[Math.floor(Math.random() * src.length)];
  }
  function randomBetween(min, max) { return Math.floor(min + Math.random() * (max - min)); }
  function pickWeightedCategory(categories, facing) {
    var cats = categories.filter(function (c) { return c.actions.length > 0; });
    if (!cats.length) return null;
    var filtered = cats.filter(function (c) { return !(c.noMirror && facing === 'right'); });
    var eligible = filtered.length ? filtered : cats;
    var totalW = eligible.reduce(function (s, c) { return s + c.weight; }, 0) || 1;
    var t = Math.random() * totalW;
    for (var i = 0; i < eligible.length; i++) { t -= eligible[i].weight; if (t <= 0) return eligible[i]; }
    return eligible[eligible.length - 1];
  }
  function rollKind(roll, w) {
    var topEnd = (w.idle + w.turn + w.move) / 100;
    if (roll < w.idle / 100) return 'idle';
    if (roll < (w.idle + w.turn) / 100) return 'turn';
    if (roll < topEnd) return 'move';
    return 'action';
  }
  function pickCategoryAction(categories, idlePool, facing, current) {
    var cat = pickWeightedCategory(categories, facing);
    if (!cat) return { id: 'FALLBACK', name: pick(idlePool, current) };
    return { id: cat.id, name: pick(cat.actions, current) };
  }

  /* ==================== 纯逻辑：移动几何（原 shared/motion.ts） ==================== */
  function planMove(o) {
    var side = o.sideAllow || 0;
    var distance = randomBetween(o.minDist, o.maxDist);
    var target = o.cx + o.dir * distance;
    var leftBound = o.margin + o.halfW - side;
    var rightBound = o.W - o.margin - o.halfW + side;
    if (target < leftBound || target > rightBound) return null;
    return {
      startRatio: o.cx / o.W,
      startYRatio: o.cy / o.H,
      targetRatio: target / o.W,
      totalRatio: Math.abs(target - o.cx) / o.W
    };
  }

  /* ==================== 纯逻辑：抛掷物理（原 shared/physics.ts） ==================== */
  function landingSquash(impactSpeed) {
    var t = Math.min(Math.max((Math.abs(impactSpeed) - SQ_SOFT_SPEED) / (SQ_HARD_SPEED - SQ_SOFT_SPEED), 0), 1);
    return Math.min(0.8, 1 - t * (1 - SQ_MAX_SQUASH));
  }
  function squashScale(u, squash) {
    squash = squash === undefined ? SQ_SQUASH : squash;
    if (u < 0.45) {
      var p = u / 0.45;
      return 1 - (1 - squash) * p * p;
    }
    var p2 = (u - 0.45) / 0.55;
    var c1 = 1.70158, c3 = c1 + 1;
    var f = 1 + c3 * Math.pow(p2 - 1, 3) + c1 * Math.pow(p2 - 1, 2);
    return Math.min(1.12, squash + (1 - squash) * Math.max(f, 0));
  }
  function throwBounds(o) {
    var h = (o.size * 9) / 16;
    return { minX: -o.sideAllow, minY: 0, maxX: o.W - o.size + o.sideAllow, maxY: o.H - h };
  }
  function trimTrail(trail, now) {
    var cutoff = now - TRAIL_KEEP_MS, i = 0;
    while (i < trail.length && trail[i].t < cutoff) i++;
    return i === 0 ? trail : trail.slice(i);
  }
  function springStep(v, x, target, dt) { return v + ((target - x) * SPRING_K - v * SPRING_C) * dt; }
  function softClampSpeed(speed) {
    if (speed <= 0) return 0;
    return MAX_THROW_SPEED * (1 - Math.exp(-speed / MAX_THROW_SPEED));
  }
  function estimateReleaseVelocity(trail, now) {
    if (!trail.length) return null;
    var last = trail[trail.length - 1];
    if (now - last.t > RELEASE_STALE_MS) return null;
    var win = trail.filter(function (s) { return now - s.t <= RELEASE_WINDOW_MS; });
    if (win.length < 2) return null;
    var t0 = win[0].t, x0 = win[0].x, y0 = win[0].y;
    var t1 = win[win.length - 1].t, x1 = win[win.length - 1].x, y1 = win[win.length - 1].y;
    var spanMs = t1 - t0;
    if (spanMs < MIN_SPAN_MS) return null;
    var baseVx = ((x1 - x0) / spanMs) * 1000;
    var baseVy = ((y1 - y0) / spanMs) * 1000;
    var baseSpeed = Math.hypot(baseVx, baseVy);
    if (baseSpeed < 1e-6) return null;
    var segSpeeds = [];
    var px = x0, py = y0, pt = t0;
    for (var i = 1; i < win.length; i++) {
      var s = win[i], dt = s.t - pt;
      if (dt >= SEG_MIN_DT_MS) {
        segSpeeds.push({ speed: (Math.hypot(s.x - px, s.y - py) / dt) * 1000, tEnd: s.t });
        px = s.x; py = s.y; pt = s.t;
      }
    }
    var peakSpeed = segSpeeds.length ? Math.max.apply(null, segSpeeds.map(function (v) { return v.speed; })) : baseSpeed;
    var accel = 0;
    if (segSpeeds.length >= 2) {
      var lastSeg = segSpeeds[segSpeeds.length - 1], firstSeg = segSpeeds[0];
      accel = (lastSeg.speed - firstSeg.speed) / Math.max((lastSeg.tEnd - firstSeg.tEnd) / 1000, MIN_SPAN_MS / 1000);
    }
    var speedBeforeClamp =
      ((1 - PEAK_WEIGHT) * baseSpeed + PEAK_WEIGHT * peakSpeed) *
      (1 + Math.min(Math.max(accel, 0) / ACCEL_REF, 1) * ACCEL_GAIN_MAX);
    var speed = softClampSpeed(speedBeforeClamp);
    if (speed < DEAD_ZONE_SPEED) return null;
    return { vx: (baseVx / baseSpeed) * speed, vy: (baseVy / baseSpeed) * speed };
  }
  function throwStep(s, dtRaw, b) {
    var dt = Math.min(Math.max(dtRaw, 0), MAX_STEP_DT);
    var x = s.x, y = s.y, vx = s.vx, vy = s.vy;
    vy += GRAVITY * dt;
    x += vx * dt;
    y += vy * dt;
    var bounced = false;
    if (x < b.minX) { x = b.minX; vx = Math.abs(vx) * RESTITUTION; bounced = true; }
    else if (x > b.maxX) { x = b.maxX; vx = -Math.abs(vx) * RESTITUTION; bounced = true; }
    if (y < b.minY) { y = b.minY; vy = Math.abs(vy) * RESTITUTION; bounced = true; }
    else if (y >= b.maxY) {
      y = b.maxY;
      vx *= Math.max(0, 1 - GROUND_FRICTION * dt);
      if (Math.abs(vy) < REST_VY) vy = 0;
      else vy = -Math.abs(vy) * RESTITUTION;
      bounced = true;
    }
    var speed = Math.hypot(vx, vy);
    var atRest =
      (y >= b.maxY - 1 && Math.abs(vy) < 1 && Math.abs(vx) < REST_VX) || (bounced && speed < REST_VY && Math.abs(vy) < 1);
    return { x: x, y: y, vx: vx, vy: vy, bounced: bounced, atRest: atRest };
  }

  /* ==================== 右键菜单（原 shared/menu.ts） ==================== */
  function isNoMirrorAnimation(categories, anim) {
    return (categories || []).some(function (c) { return c.noMirror === true && c.actions.indexOf(anim) >= 0; });
  }
  function leafOf(anim) { return { label: anim, anim: anim }; }
  function buildMenuTree(animations) {
    var groups = [];
    var pools = [
      ['待机', animations.idle],
      ['转向', animations.turn],
      ['拖拽', animations.drag],
      ['点击回应', animations.clicks],
      ['移动', animations.moves.actions.map(function (m) { return m.name; })]
    ];
    pools.forEach(function (p) {
      if (p[1].length) groups.push({ label: p[0], children: p[1].map(leafOf) });
    });
    (animations.categories || []).forEach(function (c) {
      if (c.actions.length) groups.push({ label: c.id, children: c.actions.map(leafOf) });
    });
    var EVENT_LABELS = { whisper: '碎碎念' };
    var events = animations.events || {};
    Object.keys(events).forEach(function (key) {
      var pool = events[key] || [];
      if (pool.length) groups.push({ label: EVENT_LABELS[key] || key, children: pool.map(leafOf) });
    });
    if (!groups.length) return [];
    return [{ label: '动作', children: groups }];
  }
  function isBranchNode(n) { return Array.isArray(n.children); }
  function mountContextMenu(opts) {
    var tree = opts.tree, x = opts.x, y = opts.y, onAction = opts.onAction, onClose = opts.onClose;
    var root = document.createElement('div');
    root.className = 'dsh-pet-menu';
    root.addEventListener('contextmenu', function (e) { e.preventDefault(); });
    var closed = false;
    var openChild = new Map();
    var leaveTimer = null;
    function hideChain(panel) {
      panel.style.display = 'none';
      var child = openChild.get(panel);
      if (child) { openChild.delete(panel); hideChain(child); }
    }
    function showPanel(panel, item) {
      var rect = item.getBoundingClientRect();
      var vw = window.innerWidth, vh = window.innerHeight;
      panel.style.display = 'block';
      var left = rect.right + 4;
      if (left + panel.offsetWidth > vw - 4) left = rect.left - panel.offsetWidth - 4;
      left = Math.max(4, left);
      var top = rect.top;
      if (top + panel.offsetHeight > vh - 4) top = Math.max(4, vh - 4 - panel.offsetHeight);
      panel.style.left = left + 'px';
      panel.style.top = top + 'px';
    }
    function buildPanel(nodes) {
      var panel = document.createElement('div');
      panel.className = 'dsh-pet-menu-column';
      panel.style.display = 'none';
      root.appendChild(panel);
      nodes.forEach(function (node) {
        var item = document.createElement('div');
        item.className = 'dsh-pet-menu-item';
        var label = document.createElement('span');
        label.textContent = node.label;
        item.appendChild(label);
        if (isBranchNode(node)) {
          var arrow = document.createElement('span');
          arrow.className = 'dsh-pet-menu-arrow';
          arrow.textContent = '▸';
          item.appendChild(arrow);
          var childPanel = buildPanel(node.children);
          item.addEventListener('mouseenter', function () {
            var prev = openChild.get(panel);
            if (prev && prev !== childPanel) hideChain(prev);
            openChild.set(panel, childPanel);
            showPanel(childPanel, item);
          });
        } else {
          item.addEventListener('click', function (e) {
            e.preventDefault();
            e.stopPropagation();
            close();
            onAction(node);
          });
        }
        panel.appendChild(item);
      });
      return panel;
    }
    var rootPanel = buildPanel(tree);
    rootPanel.style.display = 'block';
    document.body.appendChild(root);
    var rw = rootPanel.offsetWidth, rh = rootPanel.offsetHeight;
    rootPanel.style.left = Math.max(4, Math.min(x, window.innerWidth - rw - 4)) + 'px';
    rootPanel.style.top = Math.max(4, Math.min(y, window.innerHeight - rh - 4)) + 'px';
    root.addEventListener('mouseleave', function () {
      if (leaveTimer !== null) window.clearTimeout(leaveTimer);
      leaveTimer = window.setTimeout(function () { leaveTimer = null; close(); }, 200);
    });
    root.addEventListener('mouseover', function () {
      if (leaveTimer !== null) { window.clearTimeout(leaveTimer); leaveTimer = null; }
    });
    function onDocPointerDown(e) { if (closed || root.contains(e.target)) return; close(); }
    function onDocKeyDown(e) { if (!closed && e.key === 'Escape') close(); }
    document.addEventListener('mousedown', onDocPointerDown, true);
    document.addEventListener('keydown', onDocKeyDown, true);
    function close() {
      if (closed) return;
      closed = true;
      if (leaveTimer !== null) window.clearTimeout(leaveTimer);
      document.removeEventListener('mousedown', onDocPointerDown, true);
      document.removeEventListener('keydown', onDocKeyDown, true);
      root.remove();
      if (onClose) onClose();
    }
    return { el: root, close: close };
  }

  /* ==================== 极简 JSONC 解析（去 // 与 块注释） ==================== */
  function stripJsonc(src) {
    var out = '', i = 0, inStr = false;
    while (i < src.length) {
      var ch = src[i];
      if (inStr) {
        out += ch;
        if (ch === '\\') { out += src[i + 1] || ''; i += 2; continue; }
        if (ch === '"') inStr = false;
        i++;
        continue;
      }
      if (ch === '"') { inStr = true; out += ch; i++; continue; }
      if (ch === '/' && src[i + 1] === '/') { while (i < src.length && src[i] !== '\n') i++; continue; }
      if (ch === '/' && src[i + 1] === '*') { i += 2; while (i < src.length && !(src[i] === '*' && src[i + 1] === '/')) i++; i += 2; continue; }
      out += ch; i++;
    }
    return out;
  }

  /* ==================== 内置默认联想气泡文案（可用 options.phrases 覆盖） ==================== */
  var DEFAULT_PHRASES = [
    '今天也要加油呀', '主人，喝口水休息一下吧', '有点想睡觉了……', '代码写完了吗？',
    '好想吃火锅！', '魔方好难呀……', '外面天气怎么样呢', '我在这儿陪着你哦',
    '要不要点个奶茶？', '嗯……接下来做什么呢', '工作辛苦了！', '陪我玩一会儿嘛'
  ];

  /* ==================== 宠物实例 ==================== */
  function Pet(opts, cfg) {
    this.opts = opts;
    this.animations = cfg.animations;
    this.weights = cfg.animationWeights;
    this.size = opts.size;
    this.halfW = this.size / 2;
    this.halfH = (this.size * 9) / 16 / 2;
    this.facing = 'left';
    this.dragging = false;
    this.customPos = null;
    this.corner = opts.corner;
    this.margin = { x: opts.marginX, y: opts.marginY };
    this.sideAllow = (HIT_BOX.x0 / 640) * this.size;
    this.bottomPad = (this.size * (9 / 16) * (CANVAS_H - FEET_Y)) / CANVAS_H;

    // 播放状态
    this.anim = this.animations.idle[0] || '';
    this.once = true;
    this.front = 0;
    this.pending = null;
    this.gen = 0;

    // 拖拽 / 抛掷 / Q 弹状态
    this.drag = { active: false, dragging: false, sx: 0, sy: 0, offX: 0, offY: 0 };
    this.justDragged = false;
    this.dragTrail = [];
    this.boxPx = null;
    this.dragTarget = null;
    this.dragVel = { vx: 0, vy: 0 };
    this.dragFollowRaf = null; this.dragFollowToken = 0;
    this.throwRaf = null; this.throwToken = 0;
    this.squashRaf = null; this.squashToken = 0;
    this.pendingSquash = false;
    this.moveRaf = null; this.moveToken = 0;
    this.pendingMove = null;
    this.menuMount = null;
    this.bubbleTimer = null;
    this.whisperTimer = null;
    this._justDraggedTimer = null;
    this._responsiveMq = null;
    this._smallScreen = false;
    this._destroyed = false;

    this._buildDom();
    this._bind();
    this._initResponsive();
    this._switchTo(this.anim, this.once);
    this._startWhisperLoop();
  }

  Pet.prototype._buildDom = function () {
    var opts = this.opts;
    var root = document.createElement('div');
    root.className = 'dsh-pet-root';
    root.dataset.corner = this.corner;
    root.style.setProperty('--dsh-pet-size', this.size + 'px');
    root.style.setProperty('--dsh-pet-mx', this.margin.x + 'px');
    root.style.setProperty('--dsh-pet-my', this.margin.y + 'px');
    root.style.setProperty('--dsh-pet-z', String(opts.zIndex));

    var stage = document.createElement('div');
    stage.className = 'dsh-pet-stage';
    stage.style.transform = 'translateY(' + this.bottomPad + 'px)';

    var videoA = document.createElement('video');
    var videoB = document.createElement('video');
    [videoA, videoB].forEach(function (v) {
      v.muted = true; v.autoplay = true; v.playsInline = true;
      v.className = 'dsh-pet-video';
    });
    videoA.classList.add('is-front');

    var hit = document.createElement('div');
    hit.className = 'dsh-pet-hit';
    hit.title = opts.name;
    hit.style.left = (HIT_BOX.x0 / 640) * 100 + '%';
    hit.style.top = (HIT_BOX.y0 / 360) * 100 + '%';
    hit.style.width = ((HIT_BOX.x1 - HIT_BOX.x0) / 640) * 100 + '%';
    hit.style.height = ((HIT_BOX.y1 - HIT_BOX.y0) / 360) * 100 + '%';

    var bubble = document.createElement('div');
    bubble.className = 'dsh-pet-bubble';

    stage.appendChild(videoA);
    stage.appendChild(videoB);
    stage.appendChild(hit);
    root.appendChild(bubble);
    root.appendChild(stage);
    document.body.appendChild(root);

    this.root = root; this.stage = stage; this.hit = hit; this.bubble = bubble;
    this.videos = [videoA, videoB];
  };

  /* ---------- 小屏（移动端）隐藏 ---------- */
  Pet.prototype._initResponsive = function () {
    var self = this;
    var mv = this.opts.minViewport;
    if (!mv) return; // 显式禁用
    if (!mv.width && !mv.height) return;
    // CSS 已按默认 768/600 内置兜底；自定义阈值时按 JS 判定
    var useCss = mv.width === 768 && mv.height === 600;
    var parts = [];
    if (mv.width) parts.push('(max-width:' + (mv.width - 1) + 'px)');
    if (mv.height) parts.push('(max-height:' + (mv.height - 1) + 'px)');
    var mq = window.matchMedia(parts.join(','));
    function apply(hiddenByMq) {
      if (self._destroyed) return;
      if (useCss) self.root.classList.toggle('dsh-pet-auto-hide', hiddenByMq);
      self._setSmallScreen(hiddenByMq);
    }
    if (mq.addEventListener) {
      mq.addEventListener('change', function (e) { apply(e.matches); });
    } else if (mq.addListener) {
      mq.addListener(function (e) { apply(e.matches); });
    }
    this._responsiveMq = mq;
    apply(mq.matches);
  };

  Pet.prototype._setSmallScreen = function (on) {
    if (this._smallScreen === on) return;
    this._smallScreen = on;
    this.root.classList.toggle('dsh-pet-hidden', on);
    if (on) {
      // 进入小屏：暂停视频 / 漫游 / 甩抛，隐藏气泡与菜单
      this.videos.forEach(function (v) { v.pause(); });
      this._stopMove(); this._stopThrow(); this._stopDragFollow();
      if (this.bubbleTimer !== null) {
        window.clearTimeout(this.bubbleTimer);
        this.bubbleTimer = null;
        this.bubble.classList.remove('is-on');
      }
      if (this.menuMount) this.menuMount.close();
    } else {
      // 回到大屏：恢复播放
      this.videos.forEach(function (v) { v.play().catch(function () {}); });
    }
  };

  Pet.prototype._frontEl = function () { return this.videos[this.front]; };

  Pet.prototype._switchTo = function (next, nextOnce) {
    if (!next) return;
    var self = this;
    var pending = this.pending;
    if (pending && pending.anim === next && pending.once === nextOnce) {
      // 防重命中：目标=当前动画不重播，但消费 Q 弹标记（压当前前台视频）
      if (this.pendingSquash) {
        this.pendingSquash = false;
        this._startSquash(this._frontEl());
      }
      return;
    }
    var gen = ++this.gen;
    this.pending = { anim: next, once: nextOnce, gen: gen };
    var el = this.videos[this.front === 0 ? 1 : 0];
    el.src = this.opts.assetRoot + '/assets/webm/' + encodeURIComponent(next) + THUMB_EXT;
    el.onerror = function () {
      console.error('[dsh-pet] 动画加载失败：' + el.currentSrc +
        '（检查 assetRoot 是否可访问 / 是否被 CORS 拦截）');
    };
    el.loop = !nextOnce;
    el.muted = true;
    el.autoplay = true;
    el.playsInline = true;
    el.onended = nextOnce ? function (e) { self._handleEnded(e); } : null;
    el.load();
    function onReady() {
      el.removeEventListener('loadeddata', onReady);
      if (self._destroyed || !self.pending || self.pending.gen !== gen) return;
      var old = self._frontEl();
      el.classList.add('is-front');
      if (old && old !== el) {
        old.classList.remove('is-front');
        old.onended = null;
        old.pause();
      }
      self.front = self.front === 0 ? 1 : 0;
      self.pending = null;
      self.anim = next;
      self.once = nextOnce;
      el.style.transform = self.facing === 'right' ? 'scaleX(-1)' : '';
      el.play().catch(function () {});
      if (self.pendingSquash) {
        self.pendingSquash = false;
        self._startSquash(el);
      }
      if (self.pendingMove) self._startMoveDrive(el);
    }
    el.addEventListener('loadeddata', onReady);
    if (el.readyState >= 2) onReady();
  };

  /* ---------- 动画链：播完按权重选下一个 ---------- */
  Pet.prototype._pickNext = function () {
    var anims = this.animations, w = this.weights;
    var roll = Math.random();
    var k = rollKind(roll, w);
    var next;
    if (k === 'idle') {
      next = pick(anims.idle, this.anim);
      this._setAnim(next, true);
    } else if (k === 'turn') {
      next = pick(anims.turn, this.anim);
      this._setAnim(next, true);
    } else if (k === 'move') {
      var moved = this._tryMove();
      if (moved === false) {
        var act = pickCategoryAction(anims.categories, anims.idle, this.facing, this.anim);
        this._setAnim(act.name, true);
      }
      // moved === true：已有移动在进行，不重播
    } else {
      var act2 = pickCategoryAction(anims.categories, anims.idle, this.facing, this.anim);
      this._setAnim(act2.name, true);
    }
  };

  Pet.prototype._setAnim = function (name, once) { this._switchTo(name, once); };

  Pet.prototype._handleEnded = function (e) {
    var evEl = e && e.currentTarget;
    if (evEl && !evEl.classList.contains('is-front')) return;
    if (this._destroyed) return;
    var anims = this.animations;
    if (this.drag.active) return;
    var cur = this.anim;
    // 事件动画播完 → 回 idle
    var isEvent = Object.keys(anims.events || {}).some(function (key) {
      return (anims.events[key] || []).indexOf(cur) >= 0;
    });
    if (isEvent) { this._backToIdle(); return; }
    if (anims.turn.indexOf(cur) >= 0) {
      this.facing = this.facing === 'left' ? 'right' : 'left';
    }
    if (anims.drag.indexOf(cur) >= 0 || anims.clicks.indexOf(cur) >= 0) { this._backToIdle(); return; }
    this._pickNext();
  };

  Pet.prototype._backToIdle = function () {
    if (this.animations.idle.length) this._switchTo(pick(this.animations.idle, this.anim), false);
    else this.once = false;
  };

  /* ---------- 屏幕漫游（移动系统） ---------- */
  Pet.prototype._currentCenterX = function () {
    if (this.customPos) return this.customPos.rx * window.innerWidth;
    var rr = this.root.getBoundingClientRect();
    return rr.left + this.halfW;
  };
  Pet.prototype._currentCenterY = function () {
    if (this.customPos) return this.customPos.ry * window.innerHeight;
    var rr = this.root.getBoundingClientRect();
    return rr.top + this.halfH;
  };
  Pet.prototype._placeRoot = function (x, y) {
    this.root.style.left = x + 'px';
    this.root.style.top = y + 'px';
    this.root.style.right = 'auto';
    this.root.style.bottom = 'auto';
    this.root.dataset.corner = '';
  };

  Pet.prototype._startMoveDrive = function (el) {
    var pm = this.pendingMove;
    if (!pm || this.moveRaf !== null) return;
    this.pendingMove = null;
    var self = this;
    var duration = Number.isFinite(el.duration) && el.duration > 0 ? el.duration : 10.09;
    var travelWindow = Math.max(0.1, duration - pm.leadSec - pm.tailSec);
    var token = ++this.moveToken;
    function step() {
      if (self._destroyed || self.moveToken !== token) return;
      var t = el.currentTime || 0;
      var W = window.innerWidth, H = window.innerHeight;
      var ratioX;
      if (t <= pm.leadSec) ratioX = pm.startRatio;
      else if (t >= duration - pm.tailSec) ratioX = pm.targetRatio;
      else ratioX = pm.startRatio + pm.dir * pm.totalRatio * ((t - pm.leadSec) / travelWindow);
      self._placeRoot(ratioX * W - self.halfW, pm.startYRatio * H - self.halfH);
      if (t < duration - pm.tailSec) {
        self.moveRaf = requestAnimationFrame(step);
      } else {
        self.moveRaf = null;
        self.customPos = { rx: pm.targetRatio, ry: pm.startYRatio };
      }
    }
    this.moveRaf = requestAnimationFrame(step);
  };

  /** 尝试发起一次移动：占用中返回 true，界内挪不动返回 false，成功返回动作名 */
  Pet.prototype._tryMove = function (preferredName) {
    if (this._smallScreen) return true; // 小屏隐藏时不漫游
    if (this.moveRaf !== null || this.pendingMove || this.throwRaf !== null) return true;
    var moves = this.animations.moves;
    var actions = moves.actions;
    if (!actions.length) return false;
    var chosen = preferredName
      ? (actions.filter(function (a) { return a.name === preferredName; })[0] || null)
      : actions[Math.floor(Math.random() * actions.length)];
    if (!chosen) return false;
    var mp = Object.assign({}, moves.default, chosen.params || {});
    var dir = (this.facing === 'right') !== (this.animations.turn.indexOf(this.anim) >= 0) ? 1 : -1;
    var distScale = this.size / PET_REF_WIDTH;
    var plan = planMove({
      cx: this._currentCenterX(), cy: this._currentCenterY(),
      W: window.innerWidth, H: window.innerHeight,
      dir: dir, minDist: mp.minDist * distScale, maxDist: mp.maxDist * distScale,
      margin: mp.margin, halfW: this.halfW, sideAllow: this.sideAllow
    });
    if (!plan) return false;
    this.pendingMove = {
      startRatio: plan.startRatio, startYRatio: plan.startYRatio,
      targetRatio: plan.targetRatio, totalRatio: plan.totalRatio,
      dir: dir, leadSec: mp.leadSec, tailSec: mp.tailSec
    };
    this._switchTo(chosen.name, true);
    return chosen.name;
  };

  Pet.prototype._stopMove = function () {
    this.pendingMove = null;
    this.moveToken++;
    if (this.moveRaf !== null) { cancelAnimationFrame(this.moveRaf); this.moveRaf = null; }
  };

  /* ---------- 拖拽抛掷 ---------- */
  Pet.prototype._stopDragFollow = function () {
    this.dragFollowToken++;
    if (this.dragFollowRaf !== null) { cancelAnimationFrame(this.dragFollowRaf); this.dragFollowRaf = null; }
    this.dragTarget = null;
    this.dragVel = { vx: 0, vy: 0 };
  };
  Pet.prototype._stopThrow = function () {
    this.throwToken++;
    if (this.throwRaf !== null) { cancelAnimationFrame(this.throwRaf); this.throwRaf = null; }
  };
  Pet.prototype._startDragFollow = function () {
    if (this.dragFollowRaf !== null) return;
    var self = this;
    var token = ++this.dragFollowToken;
    var last = performance.now();
    function step() {
      if (self._destroyed || self.dragFollowToken !== token) return;
      var target = self.dragTarget;
      if (!target) { self.dragFollowRaf = null; return; }
      var now = performance.now();
      var dt = Math.min((now - last) / 1000, 1 / 30);
      last = now;
      var vel = self.dragVel;
      var x = self.boxPx ? self.boxPx.x : 0;
      var y = self.boxPx ? self.boxPx.y : 0;
      vel.vx = springStep(vel.vx, x, target.x, dt);
      vel.vy = springStep(vel.vy, y, target.y, dt);
      x += vel.vx * dt;
      y += vel.vy * dt;
      self.boxPx = { x: x, y: y };
      self._placeRoot(x, y);
      self.dragFollowRaf = requestAnimationFrame(step);
    }
    this.dragFollowRaf = requestAnimationFrame(step);
  };
  Pet.prototype._startThrow = function (px, py, vx, vy) {
    this._stopDragFollow();
    this._stopMove();
    var self = this;
    var bounds = throwBounds({ W: window.innerWidth, H: window.innerHeight, size: this.size, sideAllow: this.sideAllow });
    var token = ++this.throwToken;
    var state = { x: px, y: py, vx: vx, vy: vy };
    var last = performance.now();
    var prevGrounded = false;
    function step() {
      if (self._destroyed || self.throwToken !== token) return;
      var now = performance.now();
      var dt = (now - last) / 1000;
      last = now;
      var fallingVy = state.vy;
      var res = throwStep(state, dt, bounds);
      state = { x: res.x, y: res.y, vx: res.vx, vy: res.vy };
      self._placeRoot(res.x, res.y);
      self.boxPx = { x: res.x, y: res.y };
      self.customPos = {
        rx: (res.x + self.halfW) / window.innerWidth,
        ry: (res.y + self.halfH) / window.innerHeight
      };
      var grounded = res.y >= bounds.maxY - 1;
      if (res.bounced && grounded && !prevGrounded) {
        self._startSquash(self._frontEl(), landingSquash(fallingVy));
      }
      prevGrounded = grounded;
      if (res.atRest) { self.throwRaf = null; return; }
      self.throwRaf = requestAnimationFrame(step);
    }
    this.throwRaf = requestAnimationFrame(step);
  };

  /* ---------- Q 弹挤压 ---------- */
  Pet.prototype._startSquash = function (el, depth) {
    if (!el) return;
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    depth = depth === undefined ? SQ_SQUASH : depth;
    var self = this;
    var token = ++this.squashToken;
    if (this.squashRaf !== null) cancelAnimationFrame(this.squashRaf);
    var origin = el.style.transformOrigin;
    el.style.transformOrigin = 'bottom';
    var t0 = performance.now();
    function step() {
      if (self._destroyed || self.squashToken !== token) return;
      var u = Math.min((performance.now() - t0) / SQ_DURATION_MS, 1);
      var scale = squashScale(u, depth);
      el.style.transform = (self.facing === 'right' ? 'scaleX(-1) ' : '') + 'scaleY(' + scale + ')';
      if (u < 1) {
        self.squashRaf = requestAnimationFrame(step);
      } else {
        self.squashRaf = null;
        el.style.transformOrigin = origin;
        el.style.transform = self.facing === 'right' ? 'scaleX(-1)' : '';
      }
    }
    this.squashRaf = requestAnimationFrame(step);
  };

  /* ---------- 事件绑定：点击 / 拖拽 / 右键菜单 ---------- */
  Pet.prototype._bind = function () {
    var self = this;
    var hit = this.hit;

    hit.addEventListener('pointerdown', function (e) {
      if (e.button !== 0) return;
      self._stopThrow(); self._stopDragFollow(); self._stopMove();
      self.dragTrail = [];
      hit.classList.add('dragging');
      try { hit.setPointerCapture(e.pointerId); } catch (err) {}
      var rr = self.root.getBoundingClientRect();
      var offX = e.clientX - (rr.left + rr.width / 2);
      var offY = e.clientY - (rr.top + rr.height / 2);
      self.boxPx = { x: rr.left, y: rr.top };
      self.drag = { active: true, dragging: false, sx: e.clientX, sy: e.clientY, offX: offX, offY: offY };
    });

    hit.addEventListener('pointermove', function (e) {
      var d = self.drag;
      if (!d.active) return;
      var dx = e.clientX - d.sx, dy = e.clientY - d.sy;
      if (!d.dragging) {
        if (Math.hypot(dx, dy) < DRAG_THRESHOLD) return;
        d.dragging = true;
        self.dragging = true;
        self.stage.style.transform = 'none';
        if (self.animations.drag.length) {
          self._switchTo(pick(self.animations.drag), true);
        }
      }
      var now = performance.now();
      self.dragTrail = trimTrail(self.dragTrail.concat([{ t: now, x: e.clientX, y: e.clientY }]), now);
      self.dragTarget = { x: e.clientX - d.offX - self.halfW, y: e.clientY - d.offY - self.halfH };
      self._startDragFollow();
    });

    function onUp(e) {
      var d = self.drag;
      var wasDragging = d.dragging;
      d.active = false;
      d.dragging = false;
      hit.classList.remove('dragging');
      self._stopDragFollow();
      if (!wasDragging) return;
      self.justDragged = true;
      window.setTimeout(function () { self.justDragged = false; }, 100);
      self.dragging = false;
      self.stage.style.transform = 'translateY(' + self.bottomPad + 'px)';
      if (self.animations.idle.length) self._switchTo(pick(self.animations.idle, self.anim), false);
      var bx = self.boxPx;
      var px = bx ? bx.x : e.clientX - d.offX - self.halfW;
      var py = bx ? bx.y : e.clientY - d.offY - self.halfH;
      var vel = estimateReleaseVelocity(self.dragTrail, performance.now());
      self.dragTrail = [];
      if (vel) {
        self._startThrow(px, py, vel.vx, vel.vy);
      } else {
        self.customPos = {
          rx: (px + self.halfW) / window.innerWidth,
          ry: (py + self.halfH) / window.innerHeight
        };
        self._placeRoot(px, py);
      }
    }
    hit.addEventListener('pointerup', onUp);
    hit.addEventListener('pointercancel', onUp);

    hit.addEventListener('click', function () {
      var d = self.drag;
      if (d.active || d.dragging || self.justDragged) return;
      self._stopThrow();
      self._stopMove();
      if (!self.animations.clicks.length) return;
      self.pendingSquash = true; // 新动画就位后 Q 弹（压新首帧）
      self._switchTo(pick(self.animations.clicks), true);
    });

    hit.addEventListener('contextmenu', function (e) {
      e.preventDefault();
      e.stopPropagation();
      var d = self.drag;
      if (d.active || d.dragging || self.justDragged) return;
      var tree = [{ label: '碎碎念一句', action: 'whisper' }, { label: '回到初始位置', action: 'home' }]
        .concat(buildMenuTree(self.animations));
      if (!tree.length) return;
      self._stopThrow();
      self._stopMove();
      if (self.menuMount) self.menuMount.close();
      self.menuMount = mountContextMenu({
        tree: tree, x: e.clientX, y: e.clientY,
        onAction: function (leaf) { self._handleMenuAction(leaf); },
        onClose: function () { self.menuMount = null; }
      });
    });

    window.addEventListener('resize', function () {
      if (self.customPos && !self.throwRaf && !self.dragFollowRaf) {
        self._placeRoot(
          self.customPos.rx * window.innerWidth - self.halfW,
          self.customPos.ry * window.innerHeight - self.halfH
        );
      }
    });
  };

  Pet.prototype._handleMenuAction = function (leaf) {
    if (leaf.action === 'whisper') {
      this.say(pick(this.opts.phrases));
      var pool = (this.animations.events || {}).whisper;
      if (pool && pool.length) {
        this._stopMove();
        this._switchTo(pick(pool), true);
      }
      return;
    }
    if (leaf.action === 'home') {
      this._stopThrow();
      this._stopMove();
      this.customPos = null;
      this.root.dataset.corner = this.corner;
      this.root.style.left = '';
      this.root.style.top = '';
      this.root.style.right = '';
      this.root.style.bottom = '';
      return;
    }
    if (!leaf.anim) return;
    // 文字类（noMirror）朝右是镜像的：点播前强制朝左
    if (isNoMirrorAnimation(this.animations.categories, leaf.anim) && this.facing === 'right') {
      this.facing = 'left';
    }
    // 移动动画走真实移动；挪不动退化为纯播放
    var isMove = this.animations.moves.actions.some(function (a) { return a.name === leaf.anim; });
    if (isMove) {
      if (this._tryMove(leaf.anim) === false) {
        this._stopMove();
        this._switchTo(leaf.anim, true);
      }
      return;
    }
    this._stopMove();
    this._switchTo(leaf.anim, true);
  };

  /* ---------- 头顶联想气泡 ---------- */
  Pet.prototype.say = function (text, ms) {
    var self = this;
    if (this._smallScreen) return; // 小屏隐藏时不冒泡
    this.bubble.textContent = text;
    this.bubble.classList.add('is-on');
    if (this.bubbleTimer !== null) window.clearTimeout(this.bubbleTimer);
    this.bubbleTimer = window.setTimeout(function () {
      self.bubble.classList.remove('is-on');
      self.bubbleTimer = null;
    }, ms || BUBBLE_DURATION_MS);
  };

  Pet.prototype._startWhisperLoop = function () {
    var self = this;
    var opts = this.opts;
    if (!opts.phrases.length || !(opts.whisperIntervalSec > 0)) return;
    var whisperPool = (this.animations.events || {}).whisper || [];
    function tick() {
      if (self._destroyed) return;
      if (self._smallScreen) return; // 小屏隐藏时跳过碎碎念
      if (whisperPool.length) {
        self._stopMove();
        self._switchTo(pick(whisperPool), true);
      }
      self.say(pick(opts.phrases));
    }
    // 首次延迟一个周期，避免页面加载即冒泡
    this.whisperTimer = window.setInterval(tick, opts.whisperIntervalSec * 1000);
  };

  /* ---------- 公开点播 / 销毁 ---------- */
  Pet.prototype.play = function (name) {
    if (this._smallScreen) return; // 小屏隐藏时忽略点播
    this._stopMove();
    var isMove = this.animations.moves.actions.some(function (a) { return a.name === name; });
    if (isMove) {
      if (this._tryMove(name) === false) this._switchTo(name, true);
      return;
    }
    this._switchTo(name, true);
  };

  Pet.prototype.destroy = function () {
    this._destroyed = true;
    this._stopMove(); this._stopThrow(); this._stopDragFollow();
    this.squashToken++;
    if (this.squashRaf !== null) cancelAnimationFrame(this.squashRaf);
    if (this.bubbleTimer !== null) window.clearTimeout(this.bubbleTimer);
    if (this.whisperTimer !== null) window.clearInterval(this.whisperTimer);
    if (this._justDraggedTimer !== null) window.clearTimeout(this._justDraggedTimer);
    if (this.menuMount) this.menuMount.close();
    this.videos.forEach(function (v) { v.onended = null; v.pause(); v.removeAttribute('src'); v.load(); });
    this.root.remove();
  };

  /* ==================== 入口 ==================== */

  // 等待宿主页面完全加载（load 事件）后再启动，避免抢占宿主首屏带宽
  function waitForPageLoad(enabled) {
    if (!enabled) return Promise.resolve();
    if (document.readyState === 'complete') return Promise.resolve();
    return new Promise(function (resolve) {
      window.addEventListener('load', function () { resolve(); }, { once: true });
    });
  }

  var instance = null;

  function init(options) {
    options = options || {};
    if (instance) instance.destroy();
    var assetRoot = (options.assetRoot || 'https://pet.alcy.cc').replace(/\/+$/, '');
    var opts = {
      assetRoot: assetRoot,
      name: options.name || '蓝毛小女仆',
      size: options.size || PET_REF_WIDTH,
      corner: options.corner || 'top-right',
      marginX: options.marginX === undefined ? 24 : options.marginX,
      marginY: options.marginY === undefined ? 100 : options.marginY,
      zIndex: options.zIndex || 9999,
      minViewport: normalizeMinViewport(options.minViewport),
      phrases: Array.isArray(options.phrases) ? options.phrases.slice() : DEFAULT_PHRASES.slice(),
      whisperIntervalSec: options.whisperIntervalSec === undefined ? 300 : options.whisperIntervalSec
    };
    injectCss(assetRoot);
    var configUrl = options.configUrl || assetRoot + '/assets/config.jsonc';
    return waitForPageLoad(options.awaitLoad === undefined ? true : options.awaitLoad)
      .then(function () { return fetch(configUrl, { mode: 'cors' }); })
      .then(function (r) {
        if (!r.ok) throw new Error('[dsh-pet] 配置加载失败 HTTP ' + r.status + ': ' + configUrl);
        return r.text();
      })
      .then(function (text) {
        var cfg = JSON.parse(stripJsonc(text));
        instance = new Pet(opts, cfg);
        return api;
      })
      .catch(function (err) {
        // 素材根不可达 / 跨域被拦时给出可操作的提示
        var msg = '[dsh-pet] 初始化失败：无法加载 ' + configUrl + ' —— ' + (err && err.message || err);
        if (typeof TypeError !== 'undefined' && err instanceof TypeError) {
          msg += '\n[dsh-pet] 可能原因：'
            + '\n  1) assetRoot 指向的目录不存在（当前 assetRoot = ' + assetRoot + '）'
            + '\n  2) 跨域加载远程素材但服务器未返回 CORS 头（Access-Control-Allow-Origin: *）'
            + '\n  3) 用 file:// 打开页面 —— 请通过 HTTP 服务访问';
        }
        var e = new Error(msg);
        console.error(e);
        throw e;
      });
  }

  var api = {
    init: init,
    play: function (name) { if (instance) instance.play(name); },
    say: function (text, ms) { if (instance) instance.say(text, ms); },
    destroy: function () { if (instance) { instance.destroy(); instance = null; } },
    get instance() { return instance; }
  };

  global.DSHPet = api;
})(typeof window !== 'undefined' ? window : this);
