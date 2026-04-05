"""Map pyannote segment labels to persistent voice ids."""

from __future__ import annotations

import logging
from collections import defaultdict
from typing import Callable, Literal, Optional

import numpy as np

from app import config
from app.profiles import PendingEntry, peak_normalize
from app.registry import VoiceRegistry

log = logging.getLogger("voice")

OrderMode = Literal["temporal", "longest_first"]

_SPLIT_MIN_SEGMENTS = 4
_SPLIT_DISTANCE_THRESHOLD = float(
    getattr(config, "SPLIT_DISTANCE_THRESHOLD", 0.18)
)


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
    if not registry.pending_pool:
        return

    total = len(registry.pending_pool)
    flushed = stale = 0
    remaining = []

    for entry in registry.pending_pool:
        if entry.is_ready():
            emb = run_wavlm(entry.get_combined(), sample_rate)
            if emb is not None:
                match(emb)
                flushed += 1
            else:
                log.warning(
                    "flush_pending: WavLM returned None for ready entry (dur=%.2fs)",
                    entry.total_duration,
                )
        elif entry.is_stale():
            stale += 1
            log.debug(
                "flush_pending: dropped stale entry (dur=%.2fs, chunks_seen=%d)",
                entry.total_duration,
                entry.chunks_seen,
            )
        else:
            entry.tick()
            remaining.append(entry)

    registry.pending_pool = remaining
    if flushed or stale:
        log.info(
            "flush_pending: total=%d flushed=%d stale=%d remaining=%d",
            total, flushed, stale, len(remaining),
        )


def _cosine_distance(a: np.ndarray, b: np.ndarray) -> float:
    return 1.0 - float(
        np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b) + 1e-8)
    )


def _split_group(
    indices: list[int],
    embeddings: dict[int, np.ndarray],
) -> Optional[list[list[int]]]:
    """
    Checks a pyannote speaker group for multiple voices using HDBSCAN.
    Returns list of index groups if 2+ clusters found, else None.
    """
    embs = np.array([embeddings[i] for i in indices], dtype=np.float64)

    n = len(embs)
    max_dist = 0.0
    for i in range(n):
        for j in range(i + 1, n):
            d = _cosine_distance(embs[i], embs[j])
            if d > max_dist:
                max_dist = d
    if max_dist < _SPLIT_DISTANCE_THRESHOLD:
        return None

    log.info(
        "split_merged_speakers: group of %d segs has max_dist=%.3f > %.3f — running HDBSCAN",
        n, max_dist, _SPLIT_DISTANCE_THRESHOLD,
    )

    try:
        import hdbscan as hdbscan_lib
        from sklearn.metrics.pairwise import cosine_distances
    except ImportError:
        log.warning("split_merged_speakers: hdbscan/sklearn not available, skipping")
        return None

    dist_mat = cosine_distances(embs).astype(np.float64)
    labels = hdbscan_lib.HDBSCAN(
        min_cluster_size=3,
        metric="precomputed",
        cluster_selection_method="eom",
    ).fit_predict(dist_mat)

    unique_clusters = sorted(set(labels) - {-1})
    if len(unique_clusters) < 2:
        log.info("split_merged_speakers: HDBSCAN found only 1 cluster, skipping")
        return None

    groups = [
        [indices[i] for i, l in enumerate(labels) if l == cid]
        for cid in unique_clusters
    ]

    if any(len(g) < 2 for g in groups):
        log.info(
            "split_merged_speakers: rejected — cluster too small: %s",
            [len(g) for g in groups],
        )
        return None

    log.info(
        "split_merged_speakers: accepted — %d clusters, sizes %s (noise=%d)",
        len(groups), [len(g) for g in groups], list(labels).count(-1),
    )
    return groups


def split_merged_speakers(
    pyannote_groups: dict[str, list[int]],
    embeddings: dict[int, np.ndarray],
) -> dict[str, list[int]]:
    result: dict[str, list[int]] = {}
    split_count = 0

    for spk_label, indices in pyannote_groups.items():
        valid = [i for i in indices if i in embeddings]

        if len(valid) < _SPLIT_MIN_SEGMENTS:
            result[spk_label] = indices
            continue

        groups = _split_group(valid, embeddings)
        if groups is None:
            result[spk_label] = indices
        else:
            result[spk_label] = groups[0]
            for k, g in enumerate(groups[1:]):
                result[f"{spk_label}_split_{k}"] = g
            split_count += 1

    if split_count:
        log.info("split_merged_speakers: %d group(s) split", split_count)

    return result


def remap_speakers(
    segments: list,
    audio: np.ndarray,
    sample_rate: int,
    registry: VoiceRegistry,
    run_wavlm: Callable[[np.ndarray, int], Optional[np.ndarray]],
    order: OrderMode = "temporal",
    session_id: Optional[str] = None,
) -> list:
    def match_fn(emb: np.ndarray):
        p, _ = registry.match_or_register(emb)
        return p

    flush_pending(registry, sample_rate, run_wavlm, match_fn)

    is_full_file = order == "longest_first"

    indices = list(range(len(segments)))
    if is_full_file:
        indices.sort(
            key=lambda i: segments[i]["end"] - segments[i]["start"],
            reverse=True,
        )

    # ── Step 1: collect embeddings and pyannote grouping ──────────────────
    raw_embeddings: dict[int, np.ndarray] = {}
    pyannote_groups: dict[str, list[int]] = defaultdict(list)

    for idx in indices:
        seg = segments[idx]
        spk = seg.get("speaker", "UNKNOWN")
        if spk == "UNKNOWN":
            continue
        dur = seg["end"] - seg["start"]
        if dur < 0.3:
            continue
        chunk = audio[int(seg["start"] * sample_rate): int(seg["end"] * sample_rate)]
        chunk = peak_normalize(chunk)

        if dur >= config.MIN_SPEAKER_DURATION:
            emb = run_wavlm(chunk, sample_rate)
            if emb is not None:
                raw_embeddings[idx] = emb
                pyannote_groups[spk].append(idx)
        else:
            rough_emb = run_wavlm(chunk, sample_rate)
            add_to_pending(registry, [chunk], dur, rough_emb)

    # ── Step 2: split merged speakers (full_file only) ────────────────────
    if is_full_file and pyannote_groups:
        pyannote_groups = split_merged_speakers(
            dict(pyannote_groups), raw_embeddings
        )

    # ── Step 3: match each group against the registry ─────────────────────
    seg_to_profile: dict[int, tuple] = {}

    for spk_label, group_indices in pyannote_groups.items():
        valid = [i for i in group_indices if i in raw_embeddings]
        if not valid:
            continue

        if is_full_file:
            valid.sort(
                key=lambda i: segments[i]["end"] - segments[i]["start"],
                reverse=True,
            )

        for idx in valid:
            emb = raw_embeddings[idx]
            profile, sim = registry.match_or_register(emb)
            seg_to_profile[idx] = (profile, float(sim))

            # ── Persist embedding for long-term split detection ───────────
            registry.store.add_segment_embedding(
                profile.voice_id,
                emb,
                session_id=session_id,
            )

    # ── Step 4: write results into segments ───────────────────────────────
    for idx, seg in enumerate(segments):
        if idx in seg_to_profile:
            p, sim = seg_to_profile[idx]
            seg["speaker"] = p.display_name
            seg["voice_id"] = p.voice_id
            seg["match_score"] = sim

    return segments