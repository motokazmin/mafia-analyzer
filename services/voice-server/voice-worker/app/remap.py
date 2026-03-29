"""Map pyannote segment labels to persistent voice ids."""

from __future__ import annotations

from typing import Callable, Literal, Optional

import numpy as np

from app import config
from app.profiles import PendingEntry, peak_normalize
from app.registry import VoiceRegistry

OrderMode = Literal["temporal", "longest_first"]


def add_to_pending(
    registry: VoiceRegistry,
    chunks: list,
    duration: float,
    rough_emb: Optional[np.ndarray],
) -> None:
    if rough_emb is not None:
        best_entry = None
        best_sim = -1.0
        for entry in registry.pending_pool:
            if entry.rough_emb is None:
                continue
            sim = float(
                np.dot(rough_emb, entry.rough_emb)
                / (
                    np.linalg.norm(rough_emb)
                    * np.linalg.norm(entry.rough_emb)
                    + 1e-8
                )
            )
            if sim > best_sim:
                best_sim, best_entry = sim, entry
        if best_sim >= config.PENDING_MATCH_THRESHOLD and best_entry is not None:
            best_entry.add(chunks, duration, rough_emb)
            return
    registry.pending_pool.append(PendingEntry(chunks, duration, rough_emb))


def flush_pending(
    registry: VoiceRegistry,
    sample_rate: int,
    run_wavlm: Callable[[np.ndarray, int], Optional[np.ndarray]],
    match: Callable[[np.ndarray], object],
) -> None:
    remaining = []
    for entry in registry.pending_pool:
        if entry.is_ready():
            emb = run_wavlm(entry.get_combined(), sample_rate)
            if emb is not None:
                match(emb)
        elif entry.is_stale():
            pass
        else:
            entry.tick()
            remaining.append(entry)
    registry.pending_pool = remaining


def remap_speakers(
    segments: list,
    audio: np.ndarray,
    sample_rate: int,
    registry: VoiceRegistry,
    run_wavlm: Callable[[np.ndarray, int], Optional[np.ndarray]],
    order: OrderMode = "temporal",
) -> list:
    def match_fn(emb: np.ndarray):
        p, _ = registry.match_or_register(emb)
        return p

    flush_pending(registry, sample_rate, run_wavlm, match_fn)

    indices = list(range(len(segments)))
    if order == "longest_first":
        indices.sort(
            key=lambda i: segments[i]["end"] - segments[i]["start"],
            reverse=True,
        )

    seg_to_profile = {}

    for idx in indices:
        seg = segments[idx]
        spk = seg.get("speaker", "UNKNOWN")
        if spk == "UNKNOWN":
            continue
        dur = seg["end"] - seg["start"]
        if dur < 0.3:
            continue
        chunk = audio[int(seg["start"] * sample_rate) : int(seg["end"] * sample_rate)]
        chunk = peak_normalize(chunk)

        if dur >= config.MIN_SPEAKER_DURATION:
            emb = run_wavlm(chunk, sample_rate)
            if emb is None:
                continue
            profile, sim = registry.match_or_register(emb)
            seg_to_profile[idx] = (profile, float(sim))
        else:
            rough_emb = run_wavlm(chunk, sample_rate)
            add_to_pending(registry, [chunk], dur, rough_emb)

    for idx, seg in enumerate(segments):
        if idx in seg_to_profile:
            p, sim = seg_to_profile[idx]
            seg["speaker"] = p.display_name
            seg["voice_id"] = p.voice_id
            seg["match_score"] = sim

    return segments
