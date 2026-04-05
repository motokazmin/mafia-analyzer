"""FastAPI server: streaming chunks, voice registry API."""

from __future__ import annotations

import asyncio
import logging
import os
import tempfile
import time
import uuid
from typing import Any, Literal, Optional

import uvicorn
import whisperx
from fastapi import BackgroundTasks, FastAPI, File, Form, Header, HTTPException, UploadFile
from pydantic import BaseModel

from app import config
from app.pipeline import holder
from app.registry import VoiceRegistry
from app.remap import remap_speakers
from app.store import VoiceStore

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S",
)
log = logging.getLogger("voice")

app = FastAPI(title="Voice registry server", version="0.1.0")


@app.on_event("startup")
async def startup_event() -> None:
    log.info("startup: initializing registry...")
    get_registry()
    log.info("startup: loading ML models (this may take a minute)...")
    await asyncio.to_thread(holder.ensure_loaded)
    log.info("startup: ready")

_store: Optional[VoiceStore] = None
_registry: Optional[VoiceRegistry] = None
_jobs: dict[str, dict[str, Any]] = {}

_JOB_TTL_SEC = 7200  # 2 hours

# WS split suggestion callbacks registered by Go gateway (optional integration point)
# For now we expose the suggestions via GET /voices/split_candidates
_pending_split_suggestions: list[dict[str, Any]] = []


def _purge_stale_jobs() -> None:
    cutoff = time.time() - _JOB_TTL_SEC
    stale = [jid for jid, j in _jobs.items() if j.get("created_at", 0) < cutoff]
    for jid in stale:
        _jobs.pop(jid, None)
    if stale:
        log.warning("jobs: purged %d stale job(s) exceeding TTL", len(stale))


def get_registry() -> VoiceRegistry:
    global _store, _registry
    if _registry is None:
        _store = VoiceStore(config.DATABASE_PATH)
        _registry = VoiceRegistry(_store)
        log.info("registry: loaded %d voice(s) from store", len(_registry.profiles))
    return _registry


def check_api_key(x_api_key: str) -> None:
    if x_api_key != config.API_KEY:
        raise HTTPException(status_code=403, detail="Invalid API Key")


def apply_abs_times(
    segments: list[dict[str, Any]],
    chunk_abs_start: float,
    overlap_sec: float,
) -> list[dict[str, Any]]:
    out = []
    min_abs = chunk_abs_start + overlap_sec if chunk_abs_start > 1e-6 else 0.0
    for seg in segments:
        start = float(seg.get("start", 0))
        end = float(seg.get("end", 0))
        abs_start = chunk_abs_start + start
        abs_end = chunk_abs_start + end
        seg = dict(seg)
        seg["abs_start"] = abs_start
        seg["abs_end"] = abs_end
        if abs_start >= min_abs - 1e-3:
            out.append(seg)
    return out


def _truthy_form(val: Optional[str]) -> bool:
    if val is None:
        return False
    return val.strip().lower() in ("1", "true", "yes", "on")


def _run_pipeline(
    audio,
    reg: VoiceRegistry,
    remap_order: Literal["temporal", "longest_first"],
    chunk_abs_start: float,
    overlap_sec: float,
    num_speakers: Optional[int],
    min_speakers: Optional[int],
    max_speakers: Optional[int],
    job_id: str = "",
    session_id: Optional[str] = None,
) -> dict[str, Any]:
    prefix = f"[job {job_id[:8]}]" if job_id else ""

    t0 = time.monotonic()
    audio_sec = len(audio) / 16000
    log.info("%s transcribe+align start (audio=%.1fs)", prefix, audio_sec)
    result, _ = holder.transcribe_align(audio)
    n_segs = len(result.get("segments", []))
    log.info("%s transcribe+align done in %.1fs — %d segment(s)", prefix, time.monotonic() - t0, n_segs)

    if reg.session_max_speakers is None:
        effective = num_speakers or max_speakers
        if effective is not None:
            reg.set_session_max_speakers(effective)

    diarize_kwargs: dict[str, Any] = {}
    if min_speakers is not None:
        diarize_kwargs["min_speakers"] = min_speakers
    if max_speakers is not None:
        diarize_kwargs["max_speakers"] = max_speakers
    if "max_speakers" not in diarize_kwargs and reg.session_max_speakers is not None:
        diarize_kwargs["max_speakers"] = reg.session_max_speakers

    t1 = time.monotonic()
    log.info("%s diarize start (kwargs=%s)", prefix, diarize_kwargs)
    try:
        diarize_segments = holder.diarize(
            audio,
            min_speakers=diarize_kwargs.get("min_speakers"),
            max_speakers=diarize_kwargs.get("max_speakers"),
        )
        log.info("%s diarize done in %.1fs", prefix, time.monotonic() - t1)
    except Exception as e:
        log.warning("%s diarize FAILED in %.1fs: %s", prefix, time.monotonic() - t1, e)
        segs = apply_abs_times(result["segments"], chunk_abs_start, overlap_sec)
        return {"segments": segs}

    result = whisperx.assign_word_speakers(diarize_segments, result)

    t2 = time.monotonic()
    log.info("%s remap_speakers start (order=%s)", prefix, remap_order)
    result["segments"] = remap_speakers(
        result["segments"],
        audio,
        16000,
        reg,
        holder.run_wavlm,
        order=remap_order,
        session_id=session_id,
    )
    log.info("%s remap_speakers done in %.1fs", prefix, time.monotonic() - t2)

    segs = apply_abs_times(result["segments"], chunk_abs_start, overlap_sec)
    log.info(
        "%s pipeline done — total=%.1fs, segments=%d, profiles=%d",
        prefix, time.monotonic() - t0, len(segs), len(reg.profiles),
    )
    return {"segments": segs}


def _process_job(job_id: str, path: str, reg: VoiceRegistry, **kwargs) -> None:
    t0 = time.monotonic()
    log.info("[job %s] started", job_id[:8])
    try:
        audio = whisperx.load_audio(path)
        duration = len(audio) / 16000
        log.info("[job %s] audio loaded: %.1fs", job_id[:8], duration)

        if len(audio) < 16000:
            log.warning("[job %s] audio too short (%.2fs), skipping", job_id[:8], duration)
            result = {"segments": [], "message": "audio too short"}
        else:
            result = _run_pipeline(audio, reg, job_id=job_id, **kwargs)

        _jobs[job_id] = {"status": "done", "result": result, "created_at": time.time()}
        log.info(
            "[job %s] finished in %.1fs — %d segment(s)",
            job_id[:8], time.monotonic() - t0, len(result.get("segments", [])),
        )

        # After every successful job: check for split candidates and cache suggestions
        if result.get("segments"):
            _refresh_split_suggestions(reg)

    except Exception as e:
        log.error("[job %s] FAILED after %.1fs: %s", job_id[:8], time.monotonic() - t0, e, exc_info=True)
        _jobs[job_id] = {"status": "done", "result": {"segments": []}, "created_at": time.time()}
    finally:
        if os.path.exists(path):
            os.remove(path)
        holder.unload_cuda()


def _refresh_split_suggestions(reg: VoiceRegistry) -> None:
    """Run split analysis and cache results. Called after full-file jobs."""
    global _pending_split_suggestions
    try:
        candidates = reg.check_split_candidates()
        _pending_split_suggestions = [
            {
                "voice_id": c.voice_id,
                "display_name": c.display_name,
                "embedding_count": c.embedding_count,
                "max_pairwise_dist": c.max_pairwise_dist,
                "cluster_a_size": len(c.cluster_a),
                "cluster_b_size": len(c.cluster_b),
                "n_clusters": 2 + len(c.extra_clusters),
                "cluster_sizes": [len(cl) for cl in c.all_clusters],
            }
            for c in candidates
        ]
        if candidates:
            log.info(
                "split_suggestions: %d candidate(s) found: %s",
                len(candidates),
                [c.display_name for c in candidates],
            )
    except Exception as e:
        log.error("_refresh_split_suggestions failed: %s", e, exc_info=True)


# ── Pydantic models ────────────────────────────────────────────────────────────

class VoiceInfo(BaseModel):
    voice_id: str
    display_name: Optional[str]
    segment_count: int
    unreliable: bool = False

class VoiceFlagsBody(BaseModel):
    unreliable: bool

class MergeBody(BaseModel):
    source_id: str
    target_id: str

class RenameBody(BaseModel):
    display_name: str

class SplitBody(BaseModel):
    voice_id: str
    cluster_a: Optional[list[int]] = None
    cluster_b: Optional[list[int]] = None
    extra_clusters: Optional[list[list[int]]] = None


# ── Endpoints ──────────────────────────────────────────────────────────────────

@app.get("/health")
def health() -> dict[str, Any]:
    reg = get_registry()
    return {
        "status": "ok",
        "device": config.DEVICE,
        "gpu": config.GPU_AVAILABLE,
        "profiles": len(reg.profiles),
        "pending": len(reg.pending_pool),
    }


@app.post("/process_chunk")
async def process_chunk(
    background_tasks: BackgroundTasks,
    file: UploadFile = File(...),
    num_speakers: Optional[int] = Form(None),
    min_speakers: Optional[int] = Form(None),
    max_speakers: Optional[int] = Form(None),
    chunk_abs_start: Optional[str] = Form(None),
    overlap_sec: Optional[str] = Form(None),
    full_file: Optional[str] = Form(None),
    session_id: Optional[str] = Form(None),
    x_api_key: str = Header(...),
) -> dict[str, Any]:
    check_api_key(x_api_key)

    contents = await file.read()
    size_mb = len(contents) / 1024 / 1024
    if size_mb > config.MAX_FILE_SIZE_MB:
        raise HTTPException(status_code=413, detail="File too large")

    cas = float(chunk_abs_start or "0")
    ov = float(overlap_sec or "0")
    is_full = _truthy_form(full_file)
    remap_order: Literal["temporal", "longest_first"] = (
        "longest_first" if is_full else "temporal"
    )

    reg = get_registry()

    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
        tmp.write(contents)
        path = tmp.name

    _purge_stale_jobs()

    job_id = str(uuid.uuid4())
    _jobs[job_id] = {"status": "processing", "result": None, "created_at": time.time()}

    log.info(
        "[job %s] queued — %.2fMB, full=%s, speakers=%s, abs_start=%.1f, active_jobs=%d",
        job_id[:8], size_mb, is_full, num_speakers or max_speakers, cas, len(_jobs),
    )

    background_tasks.add_task(
        asyncio.to_thread,
        _process_job,
        job_id,
        path,
        reg,
        remap_order=remap_order,
        chunk_abs_start=cas,
        overlap_sec=ov,
        num_speakers=num_speakers,
        min_speakers=min_speakers,
        max_speakers=max_speakers,
        session_id=session_id,
    )

    return {"job_id": job_id, "status": "processing"}


@app.get("/jobs/{job_id}")
async def get_job(job_id: str, x_api_key: str = Header(...)) -> dict[str, Any]:
    check_api_key(x_api_key)
    job = _jobs.get(job_id)
    if job is None:
        raise HTTPException(status_code=404, detail="job not found")
    if job["status"] == "processing":
        return {"status": "processing"}
    result = job.pop("result")
    _jobs.pop(job_id, None)
    log.info("[job %s] result delivered — %d segment(s)", job_id[:8], len(result.get("segments", [])))
    return result


@app.post("/voices/wipe")
async def wipe_voices(x_api_key: str = Header(...)) -> dict[str, str]:
    check_api_key(x_api_key)
    global _pending_split_suggestions
    store = get_registry().store
    store.wipe_all()
    reg = get_registry()
    reg.reset_session()
    reg.load_from_store()
    _pending_split_suggestions = []
    log.info("voices wiped")
    return {"status": "ok"}

@app.post("/reset")
async def reset(x_api_key: str = Header(...)) -> dict[str, str]:
    check_api_key(x_api_key)
    reg = get_registry()
    reg.reset_session()
    holder.unload_cuda()
    log.info("session reset, CUDA cache cleared")
    return {"status": "memory cleared"}

@app.get("/voices", response_model=list[VoiceInfo])
async def list_voices(x_api_key: str = Header(...)) -> list[VoiceInfo]:
    check_api_key(x_api_key)
    store = get_registry().store
    voices = store.list_voices()
    return [
        VoiceInfo(
            voice_id=r.voice_id,
            display_name=r.display_name,
            segment_count=r.segment_count,
            unreliable=r.unreliable,
        )
        for r in voices
    ]

@app.patch("/voices/{voice_id}/flags")
async def set_voice_flags(
    voice_id: str,
    body: VoiceFlagsBody,
    x_api_key: str = Header(...),
) -> dict[str, str]:
    check_api_key(x_api_key)
    store = get_registry().store
    if not store.set_flag_unreliable(voice_id, body.unreliable):
        raise HTTPException(status_code=404, detail="voice_id not found")
    log.info("voice %s unreliable=%s", voice_id, body.unreliable)
    return {"status": "ok", "voice_id": voice_id}

@app.patch("/voices/{voice_id}")
async def rename_voice(
    voice_id: str,
    body: RenameBody,
    x_api_key: str = Header(...),
) -> dict[str, str]:
    check_api_key(x_api_key)
    reg = get_registry()
    if not reg.set_display_name(voice_id, body.display_name):
        raise HTTPException(status_code=404, detail="voice_id not found")
    log.info("voice %s renamed → '%s'", voice_id, body.display_name)
    return {"status": "ok", "voice_id": voice_id}

@app.post("/voices/merge")
async def merge_voices(
    body: MergeBody,
    x_api_key: str = Header(...),
) -> dict[str, str]:
    check_api_key(x_api_key)
    store = get_registry().store
    if not store.merge_voices(body.source_id, body.target_id):
        raise HTTPException(status_code=400, detail="merge failed")
    reg = get_registry()
    reg.load_from_store()
    log.info("voices merged: %s → %s", body.source_id, body.target_id)
    return {"status": "ok", "target_id": body.target_id}


# ── Split endpoints ────────────────────────────────────────────────────────────

@app.get("/voices/split_candidates")
async def get_split_candidates(x_api_key: str = Header(...)) -> list[dict[str, Any]]:
    """
    Return currently cached split suggestions.
    Suggestions are refreshed automatically after each full-file job.
    Trigger a fresh analysis by calling POST /voices/split_candidates/refresh.
    """
    check_api_key(x_api_key)
    return _pending_split_suggestions


@app.post("/voices/split_candidates/refresh")
async def refresh_split_candidates(x_api_key: str = Header(...)) -> dict[str, Any]:
    """Re-run split analysis synchronously and refresh the cache."""
    check_api_key(x_api_key)
    reg = get_registry()
    _refresh_split_suggestions(reg)
    return {"count": len(_pending_split_suggestions), "candidates": _pending_split_suggestions}


@app.post("/voices/split")
async def split_voice(
    body: SplitBody,
    x_api_key: str = Header(...),
) -> dict[str, Any]:
    """
    Split a voice profile into two.

    If cluster_a / cluster_b are provided (indices into stored segment embeddings,
    oldest-first), uses them directly.  Otherwise re-runs clustering on the stored
    embeddings and uses the result.

    Returns both resulting voice profiles.
    """
    check_api_key(x_api_key)
    reg = get_registry()
    global _pending_split_suggestions

    voice_id = body.voice_id
    if voice_id not in reg.profiles:
        raise HTTPException(status_code=404, detail="voice_id not found")

    cluster_a = body.cluster_a
    cluster_b = body.cluster_b
    extra_clusters = body.extra_clusters or []

    # If clusters not supplied, run analysis now
    if cluster_a is None or cluster_b is None:
        candidates = reg.check_split_candidates()
        match = next((c for c in candidates if c.voice_id == voice_id), None)
        if match is None:
            raise HTTPException(
                status_code=400,
                detail="No split candidate found for this voice_id. "
                       "Try refreshing candidates first.",
            )
        cluster_a = match.cluster_a
        cluster_b = match.cluster_b
        extra_clusters = match.extra_clusters or []

    profiles = reg.split_voice(voice_id, cluster_a, cluster_b, extra_clusters)
    if profiles is None:
        raise HTTPException(status_code=500, detail="split_voice failed internally")

    kept = profiles[0]
    new_profiles = profiles[1:]

    # Remove from pending suggestions
    _pending_split_suggestions = [
        s for s in _pending_split_suggestions if s["voice_id"] != voice_id
    ]

    log.info(
        "POST /voices/split: %s → %d profiles: %s",
        voice_id[:8], len(profiles), [p.display_name for p in profiles],
    )
    return {
        "status": "ok",
        "kept": {"voice_id": kept.voice_id, "display_name": kept.display_name},
        "new": [
            {"voice_id": p.voice_id, "display_name": p.display_name}
            for p in new_profiles
        ],
    }


@app.get("/voices/{voice_id}/segments")
async def get_voice_segments(
    voice_id: str,
    x_api_key: str = Header(...),
) -> dict[str, Any]:
    """
    Return metadata about stored segment embeddings for a voice profile.
    Embeddings themselves are not returned (too large); only count and
    whether a split is suggested.
    """
    check_api_key(x_api_key)
    reg = get_registry()
    if voice_id not in reg.profiles:
        raise HTTPException(status_code=404, detail="voice_id not found")

    count = reg.store.get_segment_embeddings_count(voice_id)
    split_suggested = any(
        s["voice_id"] == voice_id for s in _pending_split_suggestions
    )
    return {
        "voice_id": voice_id,
        "display_name": reg.profiles[voice_id].display_name,
        "stored_embeddings": count,
        "split_suggested": split_suggested,
    }


@app.delete("/voices/{voice_id}/segments")
async def detach_segments(
    voice_id: str,
    x_api_key: str = Header(...),
) -> dict[str, Any]:
    """
    Delete all stored segment embeddings for a voice profile.
    Useful to reset the split-detection history without removing the profile.
    """
    check_api_key(x_api_key)
    reg = get_registry()
    if voice_id not in reg.profiles:
        raise HTTPException(status_code=404, detail="voice_id not found")

    reg.store.delete_segment_embeddings(voice_id)

    global _pending_split_suggestions
    _pending_split_suggestions = [
        s for s in _pending_split_suggestions if s["voice_id"] != voice_id
    ]

    log.info("DELETE /voices/%s/segments: embeddings cleared", voice_id[:8])
    return {"status": "ok", "voice_id": voice_id}


if __name__ == "__main__":
    uvicorn.run(
        "app.main:app",
        host=os.environ.get("VOICE_SERVER_HOST", "0.0.0.0"),
        port=int(os.environ.get("VOICE_SERVER_PORT", "8000")),
        reload=False,
    )