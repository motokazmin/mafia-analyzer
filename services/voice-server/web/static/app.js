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
    reassignTarget: null,
    sourceFilename: "",
    gameSessionId: "",
    splitCandidates: new Set(), // voice_id профилей, предложенных к разделению
    highlightedVoiceId: null,  // voice_id залипшей подсветки
    _speakerRefreshTimer: null,
  };

  const LOG_STORAGE_KEY = "mafia_voice_ui_log_v1";
  let suppressLogPersist = false;
  let persistDebounceTimer = null;
  let _pendingSplitCandidates = []; // кэш кандидатов на разделение из voice_split_suggested

  function schedulePersistFromSegments() {
    if (suppressLogPersist) return;
    clearTimeout(persistDebounceTimer);
    persistDebounceTimer = setTimeout(function () {
      persistLogToStorage();
      persistDebounceTimer = null;
    }, 350);
  }

  const $ = (id) => document.getElementById(id);

  function persistLogToStorage() {
    if (suppressLogPersist) return;
    try {
      if (!state.segments.length) {
        localStorage.removeItem(LOG_STORAGE_KEY);
        return;
      }
      const payload = {
        version: 1,
        savedAt: Date.now(),
        gameSessionId: state.gameSessionId,
        sourceFilename: state.sourceFilename,
        colorIndex: state.colorIndex,
        speakerColors: Array.from(state.speakerColors.entries()),
        speakerNames: Array.from(state.speakerNames.entries()),
        segments: state.segments.map((s) => ({
          speaker: s.speaker,
          text: s.text,
          abs_start: s.abs_start,
          abs_end: s.abs_end,
          ts: s.ts,
          voice_id: s.voice_id,
          seq: s.seq,
          match_score: s.match_score,
          _origSpeaker: s._origSpeaker,
          _origVoiceId: s._origVoiceId,
        })),
      };
      localStorage.setItem(LOG_STORAGE_KEY, JSON.stringify(payload));
    } catch (_) {
      /* quota */
    }
  }

  function restoreLogFromStorage() {
    let raw;
    try {
      raw = localStorage.getItem(LOG_STORAGE_KEY);
    } catch (_) {
      return false;
    }
    if (!raw) return false;
    let data;
    try {
      data = JSON.parse(raw);
    } catch (_) {
      return false;
    }
    if (!data || !Array.isArray(data.segments) || !data.segments.length) return false;

    suppressLogPersist = true;
    clearLog({ skipStorage: true });
    state.gameSessionId = data.gameSessionId || "";
    state.sourceFilename = data.sourceFilename || "";
    state.colorIndex = typeof data.colorIndex === "number" ? data.colorIndex : 0;
    state.speakerColors = new Map(data.speakerColors || []);
    state.speakerNames = new Map(data.speakerNames || []);
    setSourceFileUi(state.sourceFilename || "");

    data.segments.forEach((plain) => {
      appendSegment({
        type: "segment",
        speaker: plain.speaker,
        text: plain.text,
        abs_start: plain.abs_start,
        abs_end: plain.abs_end,
        ts: plain.ts || "",
        voice_id: plain.voice_id || "",
        seq: plain.seq != null ? plain.seq : null,
        match_score: plain.match_score != null ? plain.match_score : null,
        game_session_id: state.gameSessionId,
        _origSpeaker: plain._origSpeaker,
        _origVoiceId: plain._origVoiceId,
      });
    });
    suppressLogPersist = false;
    persistLogToStorage();
    refreshSpeakersPanel();
    const hint = $("logRestoreHint");
    if (hint) {
      hint.hidden = false;
      hint.textContent =
        "Показан сохранённый лог из памяти браузера (после F5). Новый файл — кнопки «Обучить» / «Тест».";
    }
    return true;
  }

  function speakerKey(seg) {
    return seg.voice_id || seg.speaker;
  }

  function displayNameForSeg(seg) {
    return (seg.voice_id && state.speakerNames.get(seg.voice_id)) || seg.speaker;
  }

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

  function updateSegmentLine(seg) {
    const line = seg._line;
    if (!line) return;
    const displayName = displayNameForSeg(seg);
    const gray = isUnlabeledName(displayName);
    const color = gray ? null : getColorForKey(colorKeyForSeg(seg));
    const clickable = gray && !!seg.voice_id;
    line.className = "line" + (clickable ? " is-gray is-clickable" : gray ? " is-gray" : "");
    if (seg.seq != null) line.dataset.seq = String(seg.seq);

    const inner = line.querySelector(".line-inner");
    if (!inner) return;
    inner.querySelector(".line-time").textContent = seg.ts ? `[${seg.ts.slice(0, 5)}] ` : "";
    const scoreEl = inner.querySelector(".line-score");
    if (scoreEl) {
      if (seg.match_score != null && seg.match_score !== "") {
        const m = typeof seg.match_score === "number" ? seg.match_score : parseFloat(String(seg.match_score));
        scoreEl.textContent = (Number.isFinite(m) ? m.toFixed(2) : String(seg.match_score)) + " ";
        scoreEl.hidden = false;
      } else {
        scoreEl.textContent = "";
        scoreEl.hidden = true;
      }
    }
    inner.querySelector(".line-speaker").textContent = displayName + ":  ";
    inner.querySelector(".line-speaker").style.color = color || "";
    inner.querySelector(".line-text").textContent = seg.text;

    const fixBtn = line.querySelector(".line-fix");
    if (fixBtn) {
      const canFix = !!(state.gameSessionId && seg.seq && seg.voice_id);
      fixBtn.hidden = !canFix;
    }
  }

  function appendSegment(msg) {
    if (msg.seq != null && state.segments.some((s) => s.seq === msg.seq)) {
      return;
    }
    if (msg.game_session_id) {
      state.gameSessionId = msg.game_session_id;
    }
    const seg = {
      speaker: msg.speaker,
      text: msg.text,
      abs_start: msg.abs_start,
      abs_end: msg.abs_end,
      ts: msg.ts || "",
      voice_id: msg.voice_id || "",
      seq: msg.seq != null ? msg.seq : null,
      match_score: msg.match_score != null ? msg.match_score : null,
    };
    seg._origSpeaker =
      msg._origSpeaker !== undefined && msg._origSpeaker !== null ? msg._origSpeaker : seg.speaker;
    seg._origVoiceId =
      msg._origVoiceId !== undefined && msg._origVoiceId !== null
        ? msg._origVoiceId
        : seg.voice_id || "";
    state.segments.push(seg);

    const displayName = displayNameForSeg(seg);
    const gray = isUnlabeledName(displayName);
    const color = gray ? null : getColorForKey(colorKeyForSeg(seg));
    const clickable = gray && !!seg.voice_id;

    const line = document.createElement("div");
    line.className = "line" + (clickable ? " is-gray is-clickable" : gray ? " is-gray" : "");
    if (seg.seq != null) line.dataset.seq = String(seg.seq);

    const inner = document.createElement("span");
    inner.className = "line-inner";
    inner.innerHTML =
      '<span class="line-time"></span><span class="line-score"></span><span class="line-speaker"></span><span class="line-text"></span>';
    inner.querySelector(".line-time").textContent = seg.ts ? `[${seg.ts.slice(0, 5)}] ` : "";
    const scoreEl = inner.querySelector(".line-score");
    if (seg.match_score != null && seg.match_score !== "") {
      const m = typeof seg.match_score === "number" ? seg.match_score : parseFloat(String(msg.match_score));
      scoreEl.textContent = (Number.isFinite(m) ? m.toFixed(2) : String(seg.match_score)) + " ";
    } else {
      scoreEl.textContent = "";
      scoreEl.hidden = true;
    }
    inner.querySelector(".line-speaker").textContent = displayName + ":  ";
    inner.querySelector(".line-text").textContent = seg.text;
    if (!gray && color) {
      inner.querySelector(".line-speaker").style.color = color;
    }

    const fixBtn = document.createElement("button");
    fixBtn.type = "button";
    fixBtn.className = "line-fix";
    fixBtn.title = "Переназначить спикера";
    fixBtn.textContent = "✎";
    const canFix = !!(state.gameSessionId && seg.seq && seg.voice_id);
    fixBtn.hidden = !canFix;

    line.appendChild(inner);
    line.appendChild(fixBtn);
    seg._line = line;

    const log = $("log");
    log.appendChild(line);
    maybeScroll(log);
    schedulePersistFromSegments();
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
      updateSegmentLine(seg);
    });
    persistLogToStorage();
  }

  function applyMergeVoiceIds(sourceId, targetId) {
    api("/api/speakers")
      .then((list) => {
        if (!Array.isArray(list)) return;
        const tgt = list.find((v) => v.voice_id === targetId);
        let name = "";
        if (tgt && tgt.display_name) {
          name = tgt.display_name;
        }
        state.speakerNames.delete(sourceId);
        state.segments.forEach((seg) => {
          if (seg.voice_id === sourceId) {
            seg.voice_id = targetId;
            seg.speaker = name || seg.speaker;
            if (name) state.speakerNames.set(targetId, name);
            updateSegmentLine(seg);
          }
        });
        refreshSpeakersPanel();
        persistLogToStorage();
      })
      .catch(() => {});
  }

  function applySplitVoice(keptId, newIds) {
    state.splitCandidates.delete(keptId);
    _pendingSplitCandidates = _pendingSplitCandidates.filter((c) => c.voice_id !== keptId);
    refreshSpeakersPanel();
    persistLogToStorage();
  }

  function applySegmentOverrideWS(data) {
    if (data.game_session_id && data.game_session_id !== state.gameSessionId) return;
    const seq = data.seq;
    if (seq == null) return;
    state.segments.forEach((seg) => {
      if (seg.seq !== seq) return;
      if (data.cleared) {
        seg.speaker = seg._origSpeaker;
        seg.voice_id = seg._origVoiceId;
      } else if (data.speaker && data.voice_id) {
        seg.speaker = data.speaker;
        seg.voice_id = data.voice_id;
        state.speakerNames.set(data.voice_id, data.speaker);
      }
      updateSegmentLine(seg);
    });
    persistLogToStorage();
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
        } else if (data.type === "segment_override") {
          applySegmentOverrideWS(data);
        } else if (data.type === "merge") {
          applyMergeVoiceIds(data.source_id, data.target_id);
        } else if (data.type === "voice_flags") {
          refreshSpeakersPanel();
        } else if (data.type === "data_reset") {
          clearLog();
          state.splitCandidates.clear();
          _pendingSplitCandidates = [];
          refreshSpeakersPanel();
        } else if (data.type === "voice_split_suggested") {
          if (Array.isArray(data.candidates) && data.candidates.length > 0) {
            _pendingSplitCandidates = data.candidates;
            data.candidates.forEach((c) => state.splitCandidates.add(c.voice_id));
            const names = data.candidates.map((c) => c.display_name || c.voice_id.slice(0, 8)).join(", ");
            const hint = $("logRestoreHint");
            if (hint) {
              hint.hidden = false;
              hint.textContent = "🔀 Система обнаружила смешанные голоса: " + names + ". Откройте панель Спикеры для разделения.";
            }
            refreshSpeakersPanel();
          }
        } else if (data.type === "voice_split") {
          applySplitVoice(data.kept_id, data.new_ids || []);
        } else if (data.type === "voice_segments_cleared") {
          state.splitCandidates.delete(data.speaker_id);
          refreshSpeakersPanel();
        } else if (data.type === "worker_error") {
          setStopEnabled(false);
          setStatusUi("idle");
          const msg = (data.message || "").slice(0, 200);
          console.error("[worker_error]", msg);
          const hint = $("logRestoreHint");
          if (hint) {
            hint.hidden = false;
            hint.textContent = "⚠ Ошибка воркера: " + msg;
          }
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

  function clearLog(opts) {
    const skipStorage = opts && opts.skipStorage;
    state.segments = [];
    state.speakerColors.clear();
    state.speakerNames.clear();
    state.colorIndex = 0;
    state.sourceFilename = "";
    state.gameSessionId = "";
    state.highlightedVoiceId = null;
    setSourceFileUi("");
    $("log").innerHTML = "";
    const hint = $("logRestoreHint");
    if (hint) hint.hidden = true;
    if (!skipStorage) {
      try {
        localStorage.removeItem(LOG_STORAGE_KEY);
      } catch (_) {}
    }
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

  function openReassignPopup(ev, seg) {
    if (!seg.seq || !state.gameSessionId) {
      alert("Нет активной партии в БД (game_session_id).");
      return;
    }
    state.reassignTarget = seg;
    const popup = $("reassignPopup");
    const sel = $("reassignSelect");
    sel.innerHTML = "";
    const opt0 = document.createElement("option");
    opt0.value = "";
    opt0.textContent = "— выберите голос —";
    sel.appendChild(opt0);
    const optNone = document.createElement("option");
    optNone.value = "__clear__";
    optNone.textContent = "— убрать назначение —";
    sel.appendChild(optNone);
    const nameInput = $("reassignNameInput");
    if (nameInput) nameInput.value = "";
    api("/api/speakers")
      .then((list) => {
        if (!Array.isArray(list)) return;
        list.forEach((v) => {
          if (v.voice_id === seg.voice_id) return;
          const o = document.createElement("option");
          o.value = v.voice_id;
          const lab = v.display_name && String(v.display_name).trim()
            ? v.display_name
            : v.voice_id.slice(0, 8) + "…";
          o.textContent = lab + " (" + v.voice_id.slice(0, 6) + "…)";
          o.dataset.speaker = lab;
          sel.appendChild(o);
        });
      })
      .catch(() => {});
    const x = ev.clientX;
    const y = ev.clientY;
    popup.classList.remove("is-hidden");
    popup.style.left = Math.min(x, window.innerWidth - 280) + "px";
    popup.style.top = Math.min(y, window.innerHeight - 220) + "px";
  }

  function closeReassignPopup() {
    const p = $("reassignPopup");
    if (p) p.classList.add("is-hidden");
    state.reassignTarget = null;
  }

  function submitReassign() {
    const seg = state.reassignTarget;
    const sel = $("reassignSelect");
    const nameInput = $("reassignNameInput");
    if (!seg || !state.gameSessionId) return;

    const freeText = nameInput ? nameInput.value.trim() : "";
    const vid = sel ? sel.value.trim() : "";

    // Пользователь выбрал "убрать назначение"
    if (!freeText && vid === "__clear__") {
      clearReassign();
      return;
    }

    // Ничего не выбрано и не введено
    if (!freeText && !vid) return;

    // Если введено имя вручную — сохраняем оверрайд + глобально переименовываем голос (#8)
    if (freeText) {
      const path =
        "/api/games/sessions/" +
        encodeURIComponent(state.gameSessionId) +
        "/segments/" +
        encodeURIComponent(String(seg.seq)) +
        "/override";
      const voiceId = seg.voice_id || "";
      api(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ speaker: freeText, voice_id: voiceId }),
      })
        .then(() => {
          seg.speaker = freeText;
          updateSegmentLine(seg);
          closeReassignPopup();
          persistLogToStorage();
          // Также переименовываем голос глобально в Python, если voice_id известен
          if (voiceId) {
            api("/api/speakers/" + encodeURIComponent(voiceId) + "/label", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ name: freeText }),
            })
              .then(() => refreshSpeakersPanel())
              .catch((e) => console.warn("label after freeText reassign:", e));
          }
        })
        .catch((e) => alert(String(e)));
      return;
    }

    // Голос из реестра
    const opt = sel.options[sel.selectedIndex];
    const speaker = (opt && opt.dataset && opt.dataset.speaker) ? opt.dataset.speaker : vid;
    const path =
      "/api/games/sessions/" +
      encodeURIComponent(state.gameSessionId) +
      "/segments/" +
      encodeURIComponent(String(seg.seq)) +
      "/override";
    api(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ speaker: speaker, voice_id: vid }),
    })
      .then(() => {
        seg.speaker = speaker;
        seg.voice_id = vid;
        state.speakerNames.set(vid, speaker);
        updateSegmentLine(seg);
        closeReassignPopup();
        persistLogToStorage();
      })
      .catch((e) => alert(String(e)));
  }

  function clearReassign() {
    const seg = state.reassignTarget;
    if (!seg || !state.gameSessionId) return;

    function applyReset() {
      seg.speaker = seg._origSpeaker;
      seg.voice_id = seg._origVoiceId;
      updateSegmentLine(seg);
      closeReassignPopup();
      persistLogToStorage();
    }

    // Если оверрайда не было — просто закрыть, ничего не менять на бэке
    if (seg.speaker === seg._origSpeaker && seg.voice_id === seg._origVoiceId) {
      closeReassignPopup();
      return;
    }

    const path =
      "/api/games/sessions/" +
      encodeURIComponent(state.gameSessionId) +
      "/segments/" +
      encodeURIComponent(String(seg.seq)) +
      "/override";

    api(path, { method: "DELETE" })
      .then(applyReset)
      .catch((err) => {
        // 404 = оверрайда нет в БД, фронт всё равно сбрасываем
        const is404 = String(err).includes("404") || String(err).includes("Not Found");
        if (is404) {
          applyReset();
        } else {
          alert(String(err));
        }
      });
  }

  function setHighlight(voiceId) {
    state.highlightedVoiceId = voiceId;
    const logEl = $("log");
    if (!logEl) return;
    if (!voiceId) {
      logEl.classList.remove("has-highlight");
      logEl.querySelectorAll(".line.is-highlighted").forEach((el) => el.classList.remove("is-highlighted"));
      return;
    }
    logEl.classList.add("has-highlight");
    state.segments.forEach((seg) => {
      if (!seg._line) return;
      if (seg.voice_id === voiceId) {
        seg._line.classList.add("is-highlighted");
      } else {
        seg._line.classList.remove("is-highlighted");
      }
    });
    // Scroll to first highlighted line
    const first = logEl.querySelector(".line.is-highlighted");
    if (first) first.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }

  function toggleHighlight(voiceId) {
    if (state.highlightedVoiceId === voiceId) {
      setHighlight(null);
    } else {
      setHighlight(voiceId);
    }
    // Update active state on cards
    document.querySelectorAll(".speaker-card").forEach((card) => {
      card.classList.toggle("is-highlight-active", card.dataset.voiceId === voiceId && state.highlightedVoiceId === voiceId);
    });
  }

  function refreshSpeakersPanel() {
    const activeEl = document.activeElement;
    const isEditing =
      activeEl && activeEl.closest("#speakersCards input, #speakersCards select");
    if (isEditing) {
      clearTimeout(state._speakerRefreshTimer);
      state._speakerRefreshTimer = setTimeout(refreshSpeakersPanel, 2000);
      return;
    }
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
          card.dataset.voiceId = vid;
          if (v.unreliable) card.classList.add("is-unreliable");
          if (state.highlightedVoiceId === vid) card.classList.add("is-highlight-active");
          card.addEventListener("click", (e) => {
            // Не перехватываем клики по интерактивным элементам внутри карточки
            if (e.target.closest("button, input, select, label, form")) return;
            toggleHighlight(vid);
          });
          const title = labeled ? v.display_name : vid.slice(0, 8) + "…";
          const titleColor = labeled
            ? getColorForKey((v.display_name || "").trim())
            : "";
          const q = quotesByVoice[vid] || [];
          let meta = v.segment_count != null ? v.segment_count + " реплик" : "";
          if (v.unreliable) meta = (meta ? meta + " · " : "") + "под вопросом";
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
          form.className = "speaker-form";
          form.addEventListener("submit", (e) => {
            e.preventDefault();
            const inp = form.querySelector(".name-input");
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
          input.className = "name-input";
          input.placeholder = "Имя";
          input.value = labeled ? v.display_name : "";
          const btn = document.createElement("button");
          btn.type = "submit";
          btn.textContent = "Сохранить";
          form.appendChild(input);
          form.appendChild(btn);
          card.appendChild(form);

          const flagRow = document.createElement("label");
          flagRow.className = "speaker-flag";
          const cb = document.createElement("input");
          cb.type = "checkbox";
          cb.checked = !!v.unreliable;
          cb.addEventListener("change", () => {
            api("/api/speakers/" + encodeURIComponent(vid) + "/flags", {
              method: "PATCH",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ unreliable: cb.checked }),
            }).catch((err) => alert(String(err)));
          });
          flagRow.appendChild(cb);
          flagRow.appendChild(document.createTextNode(" Под вопросом (ненадёжный кластер)"));
          card.appendChild(flagRow);

          const others = list.filter((x) => x.voice_id !== vid);
          if (others.length > 0) {
            const mergeRow = document.createElement("div");
            mergeRow.className = "speaker-merge";
            const mergeSel = document.createElement("select");
            const m0 = document.createElement("option");
            m0.value = "";
            m0.textContent = "Объединить в…";
            mergeSel.appendChild(m0);
            others.forEach((o) => {
              const mo = document.createElement("option");
              mo.value = o.voice_id;
              mo.textContent = (o.display_name || o.voice_id.slice(0, 8)) + "…";
              mergeSel.appendChild(mo);
            });
            const mergeBtn = document.createElement("button");
            mergeBtn.type = "button";
            mergeBtn.textContent = "Объединить";
            mergeBtn.addEventListener("click", () => {
              const tid = mergeSel.value;
              if (!tid) return;
              if (!confirm("Склеить текущий профиль с выбранным? Исходный UUID будет удалён.")) return;
              api("/api/speakers/merge", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ source_id: vid, target_id: tid }),
              })
                .then(() => applyMergeVoiceIds(vid, tid))
                .catch((err) => alert(String(err)));
            });
            mergeRow.appendChild(mergeSel);
            mergeRow.appendChild(mergeBtn);
            card.appendChild(mergeRow);
          }

          // ── Разделить голос (если есть предложение) ──────────────────
          if (state.splitCandidates.has(vid)) {
            const candidate = (_pendingSplitCandidates || []).find((c) => c.voice_id === vid);
            const nClusters = (candidate && candidate.n_clusters) ? candidate.n_clusters : 2;
            const clusterSizes = (candidate && candidate.cluster_sizes)
              ? candidate.cluster_sizes.join(" / ")
              : "";
            const splitRow = document.createElement("div");
            splitRow.className = "speaker-split";
            const splitBtn = document.createElement("button");
            splitBtn.type = "button";
            splitBtn.className = "btn-split";
            splitBtn.textContent = "🔀 Разделить на " + nClusters + " голоса" + (clusterSizes ? " (" + clusterSizes + " сег.)" : "");
            splitBtn.title = "Система обнаружила " + nClusters + " разных голоса в этом профиле";
            splitBtn.addEventListener("click", () => {
              const displayTitle = labeled ? v.display_name : vid.slice(0, 8) + "…";
              if (!confirm(
                "Разделить «" + displayTitle + "» на два профиля?\n\n" +
                "Система автоматически распределит накопленные эмбеддинги по кластерам.\n" +
                "Сегменты в текущем логе не изменятся — переназначьте их вручную."
              )) return;
              splitBtn.disabled = true;
              splitBtn.textContent = "Разделяем…";
              api("/api/speakers/split", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ voice_id: vid }),
              })
                .then((res) => {
                  state.splitCandidates.delete(vid);
                  applySplitVoice(res.kept_id, res.new_ids || []);
                  const n = (res.new_ids || []).length + 1;
                  const hint = $("logRestoreHint");
                  if (hint) {
                    hint.hidden = false;
                    hint.textContent = "✓ Голос разделён на " + n + " профиля. Переименуйте их в панели.";
                  }
                })
                .catch((err) => {
                  splitBtn.disabled = false;
                  splitBtn.textContent = "🔀 Разделить на два голоса";
                  alert("Ошибка разделения: " + String(err));
                });
            });
            splitRow.appendChild(splitBtn);

            const dismissBtn = document.createElement("button");
            dismissBtn.type = "button";
            dismissBtn.className = "btn-muted";
            dismissBtn.title = "Сбросить историю эмбеддингов (не предлагать снова)";
            dismissBtn.textContent = "✕";
            dismissBtn.addEventListener("click", () => {
              api("/api/speakers/" + encodeURIComponent(vid) + "/segments", { method: "DELETE" })
                .then(() => {
                  state.splitCandidates.delete(vid);
                  _pendingSplitCandidates = _pendingSplitCandidates.filter((c) => c.voice_id !== vid);
                  refreshSpeakersPanel();
                })
                .catch((err) => alert(String(err)));
            });
            splitRow.appendChild(dismissBtn);
            card.appendChild(splitRow);
          }

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

  function applyLogHighlight(voiceId) {
    state.highlightedVoiceId = voiceId;
    state.segments.forEach((seg) => {
      if (!seg._line) return;
      if (voiceId === null) {
        seg._line.classList.remove("is-dimmed", "is-highlighted");
      } else if (seg.voice_id === voiceId) {
        seg._line.classList.add("is-highlighted");
        seg._line.classList.remove("is-dimmed");
      } else {
        seg._line.classList.add("is-dimmed");
        seg._line.classList.remove("is-highlighted");
      }
    });
    // Update card active state
    document.querySelectorAll(".speaker-card").forEach((card) => {
      card.classList.toggle("is-highlight-active", card.dataset.voiceId === voiceId);
    });
  }

  function toggleLogHighlight(voiceId) {
    if (state.highlightedVoiceId === voiceId) {
      applyLogHighlight(null);
    } else {
      applyLogHighlight(voiceId);
    }
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
    restoreLogFromStorage();

    const logEl = $("log");
    logEl.addEventListener("scroll", onLogScroll);
    logEl.addEventListener("click", (e) => {
      const fix = e.target.closest(".line-fix");
      if (fix) {
        e.stopPropagation();
        e.preventDefault();
        const line = fix.closest(".line");
        const seq = line && line.dataset.seq ? parseInt(line.dataset.seq, 10) : NaN;
        const seg = state.segments.find((s) => s.seq === seq);
        if (seg) openReassignPopup(e, seg);
        return;
      }
      const line = e.target.closest(".line.is-clickable");
      if (line) {
        e.stopPropagation();
        const seq = line.dataset.seq ? parseInt(line.dataset.seq, 10) : NaN;
        const seg = state.segments.find((s) => s.seq === seq);
        if (seg) openLabelPopup(e, seg);
      }
    });

    connectWs();
    api("/api/session/status")
      .then((st) => {
        if (!st) return;
        if (st.status) setStatusUi(st.status);
        const busy = st.status === "running" || st.status === "processing";
        setStopEnabled(busy);
        if (state.segments.length > 0 && !busy) {
          return;
        }
        if (st.source_filename) {
          state.sourceFilename = st.source_filename;
          setSourceFileUi(st.source_filename);
        }
        if (st.game_session_id) state.gameSessionId = st.game_session_id;
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

    const btnArchive = $("btnArchive");
    if (btnArchive) btnArchive.addEventListener("click", openArchive);
    const btnArchiveClose = $("btnArchiveClose");
    if (btnArchiveClose) btnArchiveClose.addEventListener("click", closeArchive);
    const archiveOverlay = $("archiveOverlay");
    if (archiveOverlay) {
      archiveOverlay.addEventListener("click", (e) => {
        if (e.target === archiveOverlay) closeArchive();
      });
    }
    const btnWipe = $("btnWipeData");
    if (btnWipe) {
      btnWipe.addEventListener("click", () => {
        if (
          !confirm(
            "Удалить все сохранённые партии и все голоса в реестре?\n\n" +
              "Gateway: журнал игр (SQLite). Voice-worker: эмбеддинги (SQLite).\n" +
              "Сессия должна быть остановлена."
          )
        ) {
          return;
        }
        // Чистим UI и localStorage сразу — до запроса, чтобы F5 не вернул старый лог
        clearLog();
        refreshSpeakersPanel();
        api("/api/data/reset", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ confirm: true }),
        })
          .catch((e) => alert("Ошибка очистки баз: " + String(e)));
      });
    }
    $("labelOk").addEventListener("click", submitLabel);
    const reOk = $("reassignOk");
    const reCl = $("reassignClear");
    if (reOk) reOk.addEventListener("click", submitReassign);
    if (reCl) reCl.addEventListener("click", clearReassign);

    document.addEventListener("click", (e) => {
      if (!e.target.closest("#labelPopup")) closeLabelPopup();
      if (!e.target.closest("#reassignPopup")) closeReassignPopup();
    });
    $("labelPopup").addEventListener("click", (e) => e.stopPropagation());
    const rp = $("reassignPopup");
    if (rp) rp.addEventListener("click", (e) => e.stopPropagation());

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
          if (!st) return;
          if (st.status) setStatusUi(st.status);
          const busy = st.status === "running" || st.status === "processing";
          setStopEnabled(busy);
          if (!(state.segments.length > 0 && !busy)) {
            if (st.source_filename) {
              state.sourceFilename = st.source_filename;
              setSourceFileUi(st.source_filename);
            }
            if (st.game_session_id) state.gameSessionId = st.game_session_id;
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
  // ─── Архив игр ────────────────────────────────────────────────────────────

  function openArchive() {
    const overlay = $("archiveOverlay");
    if (!overlay) return;
    overlay.classList.remove("is-hidden");
    const list = $("archiveList");
    list.innerHTML = '<div class="archive-loading">Загрузка…</div>';

    api("/api/games/sessions?limit=100")
      .then((sessions) => {
        list.innerHTML = "";
        if (!Array.isArray(sessions) || sessions.length === 0) {
          list.innerHTML = '<div class="archive-empty">Нет сохранённых игр</div>';
          return;
        }
        sessions.forEach((s) => {
          const item = document.createElement("div");
          item.className = "archive-item";

          const date = new Date(s.started_at);
          const dateStr = date.toLocaleDateString("ru-RU", {
            day: "2-digit", month: "2-digit", year: "numeric",
            hour: "2-digit", minute: "2-digit",
          });

          const modeLabels = { ingest: "Обучение", file: "Файл", record: "Запись" };
          const modeLabel = modeLabels[s.session_mode] || s.session_mode;
          const fileName = s.source_filename ? " · " + s.source_filename : "";
          const endedLabel = s.ended_at ? "" : " · идёт";

          item.innerHTML =
            '<div class="archive-item-meta">' +
            '<span class="archive-item-date">' + escapeHtml(dateStr) + '</span>' +
            '<span class="archive-item-mode">' + escapeHtml(modeLabel + fileName + endedLabel) + '</span>' +
            '</div>' +
            '<button type="button" class="archive-item-load">Открыть</button>';

          item.querySelector(".archive-item-load").addEventListener("click", () => {
            loadArchivedSession(s);
          });

          list.appendChild(item);
        });
      })
      .catch((e) => {
        list.innerHTML = '<div class="archive-empty">Ошибка загрузки: ' + escapeHtml(String(e)) + '</div>';
      });
  }

  function closeArchive() {
    const overlay = $("archiveOverlay");
    if (overlay) overlay.classList.add("is-hidden");
  }

  function loadArchivedSession(sessionMeta) {
    if (!confirm(
      'Открыть игру от ' + new Date(sessionMeta.started_at).toLocaleString("ru-RU") + '?\n' +
      'Текущий лог будет очищен.'
    )) return;

    closeArchive();

    api("/api/games/sessions/" + encodeURIComponent(sessionMeta.id) + "/segments")
      .then((segments) => {
        if (!Array.isArray(segments)) throw new Error("bad response");

        suppressLogPersist = true;
        clearLog({ skipStorage: true });

        state.gameSessionId = sessionMeta.id;
        state.sourceFilename = sessionMeta.source_filename || "";
        setSourceFileUi(state.sourceFilename);

        // Восстановить hint в строке статуса
        const hint = $("logRestoreHint");
        if (hint) {
          hint.hidden = false;
          const date = new Date(sessionMeta.started_at).toLocaleString("ru-RU");
          hint.textContent = "Архивная игра от " + date + " (режим редактирования, CUDA не нужна)";
        }

        segments.forEach((raw) => {
          // Данные из БД уже имеют применённые overrides;
          // _origSpeaker = speaker (исходник недоступен отдельно без join)
          appendSegment({
            type: "segment",
            speaker: raw.speaker,
            text: raw.text,
            abs_start: raw.abs_start,
            abs_end: raw.abs_end,
            ts: raw.wall_time || "",
            voice_id: raw.voice_id || "",
            seq: raw.seq,
            match_score: raw.match_score != null ? raw.match_score : null,
            game_session_id: sessionMeta.id,
            // _origSpeaker совпадает с speaker — данные уже «финальные»
            _origSpeaker: raw.speaker,
            _origVoiceId: raw.voice_id || "",
          });
        });

        suppressLogPersist = false;
        persistLogToStorage();
        refreshSpeakersPanel();
      })
      .catch((e) => alert("Ошибка загрузки игры: " + String(e)));
  }

})();