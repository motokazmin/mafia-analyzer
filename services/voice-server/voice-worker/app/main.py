"""FastAPI server: streaming chunks, voice registry API."""

from __future__ import annotations

import os
import tempfile
from typing import Any, Literal, Optional

import uvicorn
import whisperx
from fastapi import FastAPI, File, Form, Header, HTTPException, UploadFile
from pydantic import BaseModel

from app import config
from app.pipeline import holder
from app.registry import VoiceRegistry
from app.remap import remap_speakers
from app.store import VoiceStore

app = FastAPI(title="Voice registry server", version="0.1.0")

_store: Optional[VoiceStore] = None
_registry: Optional[VoiceRegistry] = None


def get_registry() -> VoiceRegistry:
    global _store, _registry
    if _registry is None:
        _store = VoiceStore()
        _registry = VoiceRegistry(_store)
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
) -> dict[str, Any]:
    result, _ = holder.transcribe_align(audio)

    if reg.session_max_speakers is None:
        if num_speakers is not None:
            reg.session_max_speakers = num_speakers
        elif max_speakers is not None:
            reg.session_max_speakers = max_speakers

    diarize_kwargs: dict[str, Any] = {}
    if min_speakers is not None:
        diarize_kwargs["min_speakers"] = min_speakers
    if max_speakers is not None:
        diarize_kwargs["max_speakers"] = max_speakers
    if "max_speakers" not in diarize_kwargs and reg.session_max_speakers is not None:
        diarize_kwargs["max_speakers"] = reg.session_max_speakers

    try:
        diarize_segments = holder.diarize(
            audio,
            min_speakers=diarize_kwargs.get("min_speakers"),
            max_speakers=diarize_kwargs.get("max_speakers"),
        )
    except Exception:
        segs = apply_abs_times(result["segments"], chunk_abs_start, overlap_sec)
        return {"segments": segs}

    result = whisperx.assign_word_speakers(diarize_segments, result)
    result["segments"] = remap_speakers(
        result["segments"],
        audio,
        16000,
        reg,
        holder.run_wavlm,
        order=remap_order,
    )
    segs = apply_abs_times(result["segments"], chunk_abs_start, overlap_sec)
    return {"segments": segs}



class VoiceInfo(BaseModel):
    voice_id: str
    display_name: Optional[str]
    segment_count: int


class MergeBody(BaseModel):
    source_id: str
    target_id: str


class RenameBody(BaseModel):
    display_name: str


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/reset")
async def reset(x_api_key: str = Header(...)) -> dict[str, str]:
    check_api_key(x_api_key)
    reg = get_registry()
    reg.reset_session()
    holder.unload_cuda()
    return {"status": "memory cleared"}


@app.get("/voices", response_model=list[VoiceInfo])
async def list_voices(x_api_key: str = Header(...)) -> list[VoiceInfo]:
    check_api_key(x_api_key)
    store = get_registry().store
    return [
        VoiceInfo(
            voice_id=r.voice_id,
            display_name=r.display_name,
            segment_count=r.segment_count,
        )
        for r in store.list_voices()
    ]


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
    return {"status": "ok", "target_id": body.target_id}


@app.post("/process_chunk")
async def process_chunk(
    file: UploadFile = File(...),
    num_speakers: Optional[int] = Form(None),
    min_speakers: Optional[int] = Form(None),
    max_speakers: Optional[int] = Form(None),
    chunk_abs_start: Optional[str] = Form(None),
    overlap_sec: Optional[str] = Form(None),
    full_file: Optional[str] = Form(
        None,
        description="true = full file, remap longest_first.",
    ),
    x_api_key: str = Header(...),
) -> dict[str, Any]:
    check_api_key(x_api_key)
    contents = await file.read()
    max_bytes = config.MAX_FILE_SIZE_MB * 1024 * 1024
    if len(contents) > max_bytes:
        raise HTTPException(status_code=413, detail="File too large")

    cas = float(chunk_abs_start or "0")
    ov = float(overlap_sec or "0")
    is_full = _truthy_form(full_file)
    remap_order: Literal["temporal", "longest_first"] = (
        "longest_first" if is_full else "temporal"
    )

    reg = get_registry()
    holder.ensure_loaded()

    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as tmp:
        tmp.write(contents)
        path = tmp.name

    try:
        audio = whisperx.load_audio(path)
        if len(audio) < 32000:
            msg = "audio too short"
            return {"segments": [], "message": msg} if is_full else {"segments": []}

        return _run_pipeline(
            audio,
            reg,
            remap_order,
            cas,
            ov,
            num_speakers,
            min_speakers,
            max_speakers,
        )
    except Exception as e:
        print(f">>> process_chunk error: {e}")
        holder.unload_cuda()
        return {"segments": []}
    finally:
        if os.path.exists(path):
            os.remove(path)
        holder.unload_cuda()


def main() -> None:
    uvicorn.run(
        "app.main:app",
        host=os.environ.get("VOICE_SERVER_HOST", "0.0.0.0"),
        port=int(os.environ.get("VOICE_SERVER_PORT", "8000")),
        reload=False,
    )


if __name__ == "__main__":
    main()
