(function () {
  "use strict";

  const PALETTE = [
    "#5b8cff", "#22c55e", "#eab308", "#f97316", "#a855f7",
    "#ec4899", "#14b8a6", "#ef4444", "#84cc16", "#06b6d4",
  ];

  const state = {
    segments: [],
    speakerColors: new Map(),
    speakerNames: new Map(),
    colorIndex: 0,
    ws: null,
    wsTimer: null,
    pendingIngest: false,
    atBottom: true,
    labelTarget: null,
    sourceFilename: "",
  };

  const $ = (id) => document.getElementById(id);

  function speakerKey(seg) {
    return seg.voice_id || seg.speaker;
  }

  function displayNameForSeg(seg) {
    return (seg.voice_id && state.speakerNames.get(seg.voice_id)) || seg.speaker;
  }

  /** Один цвет на отображаемое имя: все voice_id с тем же именем получают один цвет. */
  function colorKeyForSeg(seg) {
    const dn = displayNameForSeg(seg);
    if (isUnlabeledName(dn)) {
      return speakerKey(seg);
    }
    return (dn || "").trim();
  }

  function isUnlabeledName(name) {
    if (!name) return true;
    return /^Игрок_\d+$/.test(name) || /^SPEAKER_\d+$/i.test(name) || /^voice_/i.test(name);
  }

  function getColorForKey(key) {
    if (!state.speakerColors.has(key)) {
      const c = PALETTE[state.colorIndex % PALETTE.length];
      state.colorIndex++;
      state.speakerColors.set(key, c);
    }
    return state.speakerColors.get(key);
  }

  function setSourceFileUi(name) {
    const el = $("sourceFileLabel");
    if (!el) return;
    if (name) {
      el.textContent = "Файл: " + name;
      el.hidden = false;
    } else {
      el.textContent = "";
      el.hidden = true;
    }
  }

  function setStatusUi(status) {
    const dot = $("statusDot");
    const text = $("statusText");
    let label = "Ожидание";
    if (status === "running") label = "Обработка / запись";
    else if (status === "processing") label = "Обработка";
    dot.dataset.state = status === "idle" ? "idle" : "running";
    text.textContent = label;
  }

  function api(path, opts) {
    return fetch(path, opts).then((r) => {
      if (!r.ok) return r.text().then((t) => { throw new Error(t || r.status); });
      const ct = r.headers.get("content-type") || "";
      if (ct.includes("application/json")) return r.json();
      return r.text();
    });
  }

  function appendSegment(msg) {
    const seg = {
      speaker: msg.speaker,
      text: msg.text,
      abs_start: msg.abs_start,
      abs_end: msg.abs_end,
      ts: msg.ts || "",
      voice_id: msg.voice_id || "",
    };
    state.segments.push(seg);

    const displayName = displayNameForSeg(seg);
    const gray = isUnlabeledName(displayName);
    const color = gray ? null : getColorForKey(colorKeyForSeg(seg));
    const clickable = gray && !!seg.voice_id;

    const line = document.createElement("div");
    line.className = "line" + (clickable ? " is-gray is-clickable" : gray ? " is-gray" : "");
    if (clickable) {
      line.dataset.voiceId = seg.voice_id;
      line.dataset.speaker = seg.speaker;
    }

    const inner = document.createElement("span");
    inner.className = "line-inner";
    const timePart = seg.ts ? `[${seg.ts.slice(0, 5)}] ` : "";
    inner.innerHTML =
      '<span class="line-time"></span><span class="line-speaker"></span><span class="line-text"></span>';
    inner.querySelector(".line-time").textContent = timePart;
    inner.querySelector(".line-speaker").textContent = displayName + ":  ";
    inner.querySelector(".line-text").textContent = seg.text;
    if (!gray && color) {
      inner.querySelector(".line-speaker").style.color = color;
    }
    line.appendChild(inner);
    seg._line = line;

    if (clickable) {
      line.addEventListener("click", (e) => {
        e.stopPropagation();
        openLabelPopup(e, seg);
      });
    }

    const log = $("log");
    log.appendChild(line);
    maybeScroll(log);
  }

  function maybeScroll(log) {
    if (!state.atBottom) return;
    log.scrollTop = log.scrollHeight;
  }

  function onLogScroll() {
    const log = $("log");
    const thresh = 80;
    state.atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < thresh;
  }

  function applyLabel(speakerId, name) {
    state.speakerNames.set(speakerId, name);
    state.segments.forEach((seg) => {
      if (seg.voice_id !== speakerId) return;
      seg.speaker = name;
      const line = seg._line;
      if (!line) return;
      line.classList.remove("is-gray", "is-clickable");
      const ck = colorKeyForSeg(seg);
      const color = getColorForKey(ck);
      const inner = line.querySelector(".line-inner");
      inner.querySelector(".line-speaker").style.color = color;
      inner.querySelector(".line-speaker").textContent = name + ":  ";
      inner.querySelector(".line-text").textContent = seg.text;
    });
  }

  function connectWs() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const url = proto + "//" + location.host + "/ws";
    try {
      const ws = new WebSocket(url);
      state.ws = ws;
      ws.onopen = () => {
        if (state.wsTimer) {
          clearTimeout(state.wsTimer);
          state.wsTimer = null;
        }
      };
      ws.onmessage = (ev) => {
        let data;
        try {
          data = JSON.parse(ev.data);
        } catch {
          return;
        }
        if (data.type === "segment") appendSegment(data);
        else if (data.type === "status") setStatusUi(data.status);
        else if (data.type === "label") {
          applyLabel(data.speaker_id, data.name);
          refreshSpeakersPanel();
        }
      };
      ws.onclose = () => {
        state.ws = null;
        state.wsTimer = setTimeout(connectWs, 2000);
      };
      ws.onerror = () => {
        try { ws.close(); } catch (_) {}
      };
    } catch (_) {
      state.wsTimer = setTimeout(connectWs, 2000);
    }
  }

  function getSpeakersNum() {
    const v = $("speakersInput").value.trim();
    if (!v) return 0;
    const n = parseInt(v, 10);
    return Number.isFinite(n) ? n : 0;
  }

  function clearLog() {
    state.segments = [];
    state.speakerColors.clear();
    state.speakerNames.clear();
    state.colorIndex = 0;
    state.sourceFilename = "";
    setSourceFileUi("");
    $("log").innerHTML = "";
  }

  function startIngest(file) {
    clearLog();
    const fd = new FormData();
    fd.append("file", file);
    const n = getSpeakersNum();
    if (n > 0) fd.append("speakers", String(n));
    state.pendingIngest = true;
    return api("/api/ingest", { method: "POST", body: fd }).then((res) => {
      if (res && res.source_filename) {
        state.sourceFilename = res.source_filename;
        setSourceFileUi(res.source_filename);
      }
    });
  }

  function uploadThenFile(file) {
    clearLog();
    const fd = new FormData();
    fd.append("file", file);
    const origName = file.name || "";
    return api("/api/upload", { method: "POST", body: fd }).then((res) => {
      const body = {
        mode: "file",
        file_path: res.file_path,
        speakers: getSpeakersNum(),
        source_filename: origName || res.filename || "",
      };
      return api("/api/session/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      }).then(() => {
        if (body.source_filename) {
          state.sourceFilename = body.source_filename;
          setSourceFileUi(body.source_filename);
        }
      });
    });
  }

  function startRecord() {
    clearLog();
    const body = { mode: "record", speakers: getSpeakersNum() };
    return api("/api/session/start", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  }

  function stopSession() {
    return api("/api/session/stop", { method: "POST" });
  }

  function knownNamesList() {
    const s = new Set();
    state.speakerNames.forEach((v) => s.add(v));
    state.segments.forEach((seg) => {
      if (!isUnlabeledName(seg.speaker)) s.add(seg.speaker);
    });
    return Array.from(s).sort();
  }

  function openLabelPopup(ev, seg) {
    if (!seg.voice_id) return;
    const popup = $("labelPopup");
    $("labelPopupTitle").textContent = seg.speaker;
    const sel = $("labelSelect");
    sel.innerHTML = "";
    const opt0 = document.createElement("option");
    opt0.value = "";
    opt0.textContent = "— выберите —";
    sel.appendChild(opt0);
    knownNamesList().forEach((n) => {
      const o = document.createElement("option");
      o.value = n;
      o.textContent = n;
      sel.appendChild(o);
    });
    $("labelNewInput").value = "";
    const x = ev.clientX;
    const y = ev.clientY;
    popup.classList.remove("is-hidden");
    popup.style.left = Math.min(x, window.innerWidth - 240) + "px";
    popup.style.top = Math.min(y, window.innerHeight - 200) + "px";
    state.labelTarget = seg;
  }

  function closeLabelPopup() {
    $("labelPopup").classList.add("is-hidden");
    state.labelTarget = null;
  }

  function submitLabel() {
    const seg = state.labelTarget;
    if (!seg || !seg.voice_id) return;
    const sel = $("labelSelect").value.trim();
    const inp = $("labelNewInput").value.trim();
    const name = inp || sel;
    if (!name) return;
    api("/api/speakers/" + encodeURIComponent(seg.voice_id) + "/label", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    })
      .then(() => {
        applyLabel(seg.voice_id, name);
        closeLabelPopup();
        refreshSpeakersPanel();
      })
      .catch((e) => alert(String(e)));
  }

  function refreshSpeakersPanel() {
    return api("/api/speakers")
      .then((list) => {
        const panel = $("speakersPanel");
        const cards = $("speakersCards");
        if (!Array.isArray(list) || list.length === 0) {
          panel.classList.add("is-hidden");
          return;
        }
        panel.classList.remove("is-hidden");
        cards.innerHTML = "";
        const quotesByVoice = {};
        state.segments.forEach((s) => {
          if (!s.voice_id || !s.text) return;
          if (!quotesByVoice[s.voice_id]) quotesByVoice[s.voice_id] = [];
          if (quotesByVoice[s.voice_id].length < 2) quotesByVoice[s.voice_id].push(s.text);
        });
        list.forEach((v) => {
          const vid = v.voice_id;
          const labeled = v.display_name && !isUnlabeledName(v.display_name);
          const card = document.createElement("div");
          card.className = "speaker-card" + (labeled ? " is-labeled" : "");
          const title = labeled ? v.display_name : vid.slice(0, 8) + "…";
          const titleColor = labeled
            ? getColorForKey((v.display_name || "").trim())
            : "";
          const q = quotesByVoice[vid] || [];
          const meta = (v.segment_count != null ? v.segment_count + " реплик" : "");
          card.innerHTML =
            "<h3" +
            (titleColor ? ' style="color:' + titleColor + '"' : "") +
            ">" +
            escapeHtml(title) +
            "</h3>" +
            '<div class="speaker-meta">' +
            escapeHtml(meta) +
            "</div>" +
            '<div class="speaker-quotes">' +
            q.map((t) => "«" + escapeHtml(t.slice(0, 80)) + "»").join("<br>") +
            "</div>";
          const form = document.createElement("form");
          form.addEventListener("submit", (e) => {
            e.preventDefault();
            const inp = form.querySelector("input");
            const name = inp.value.trim();
            if (!name) return;
            api("/api/speakers/" + encodeURIComponent(vid) + "/label", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ name }),
            })
              .then(() => {
                applyLabel(vid, name);
                refreshSpeakersPanel();
              })
              .catch((err) => alert(String(err)));
          });
          const input = document.createElement("input");
          input.type = "text";
          input.placeholder = "Имя";
          input.value = labeled ? v.display_name : "";
          const btn = document.createElement("button");
          btn.type = "submit";
          btn.textContent = "Сохранить";
          form.appendChild(input);
          form.appendChild(btn);
          card.appendChild(form);
          cards.appendChild(card);
        });
      })
      .catch(() => {});
  }

  function escapeHtml(s) {
    const d = document.createElement("div");
    d.textContent = s;
    return d.innerHTML;
  }

  function saveLogFile() {
    const lines = state.segments.map((s) => {
      const t = s.ts ? s.ts.slice(0, 5) : "??:??";
      return "[" + t + "] " + s.speaker + ": " + s.text;
    });
    let text = lines.join("\n");
    if (state.sourceFilename) {
      text = "# Файл: " + state.sourceFilename + "\n\n" + text;
    }
    const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "mafia-log.txt";
    a.click();
    URL.revokeObjectURL(a.href);
  }

  function setStopEnabled(on) {
    $("btnStop").disabled = !on;
  }

  function pickFile(mode) {
    const input = $("fileInput");
    input.onchange = () => {
      const f = input.files && input.files[0];
      input.value = "";
      if (!f) return;
      setStopEnabled(true);
      if (mode === "ingest") {
        startIngest(f).catch((e) => alert(String(e)));
      } else {
        uploadThenFile(f).catch((e) => alert(String(e)));
      }
    };
    input.click();
  }

  document.addEventListener("DOMContentLoaded", () => {
    $("log").addEventListener("scroll", onLogScroll);
    connectWs();
    api("/api/session/status")
      .then((st) => {
        if (st && st.status) setStatusUi(st.status);
        if (st && st.source_filename) {
          state.sourceFilename = st.source_filename;
          setSourceFileUi(st.source_filename);
        }
      })
      .catch(() => {});

    $("btnIngest").addEventListener("click", () => pickFile("ingest"));
    $("btnFile").addEventListener("click", () => pickFile("file"));
    $("btnRecord").addEventListener("click", () => {
      setStopEnabled(true);
      startRecord().catch((e) => alert(String(e)));
    });
    $("btnStop").addEventListener("click", () => {
      stopSession().catch((e) => alert(String(e)));
      setStopEnabled(false);
    });
    $("btnSaveLog").addEventListener("click", saveLogFile);
    $("labelOk").addEventListener("click", submitLabel);

    document.addEventListener("click", (e) => {
      if (!e.target.closest("#labelPopup")) closeLabelPopup();
    });
    $("labelPopup").addEventListener("click", (e) => e.stopPropagation());

    const wrap = document.querySelector(".log-wrap");
    ["dragenter", "dragover"].forEach((ev) => {
      wrap.addEventListener(ev, (e) => {
        e.preventDefault();
        wrap.classList.add("drop-active");
      });
    });
    ["dragleave", "drop"].forEach((ev) => {
      wrap.addEventListener(ev, (e) => {
        e.preventDefault();
        wrap.classList.remove("drop-active");
      });
    });
    wrap.addEventListener("drop", (e) => {
      const f = e.dataTransfer.files && e.dataTransfer.files[0];
      if (!f) return;
      setStopEnabled(true);
      startIngest(f).catch((err) => alert(String(err)));
    });

    setInterval(() => {
      api("/api/session/status")
        .then((st) => {
          if (st && st.status) setStatusUi(st.status);
          const busy = st.status === "running" || st.status === "processing";
          setStopEnabled(busy);
          if (st && st.source_filename) {
            state.sourceFilename = st.source_filename;
            setSourceFileUi(st.source_filename);
          }
          if (state.pendingIngest && st.status === "idle") {
            state.pendingIngest = false;
            refreshSpeakersPanel();
          }
          if (st.status === "processing") state.pendingIngest = true;
        })
        .catch(() => {});
    }, 1500);
  });
})();
