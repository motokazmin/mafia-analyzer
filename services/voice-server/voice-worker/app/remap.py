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

# ── Параметры разделения слипшихся спикеров ───────────────────────────────────
# Минимум сегментов в группе чтобы пытаться делить
_SPLIT_MIN_SEGMENTS = 4
# Если максимальное попарное косинусное расстояние внутри группы
# превышает этот порог — считаем что там два голоса
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
) -> Optional[tuple[list[int], list[int]]]:
    """
    Проверяет группу сегментов одного pyannote-спикера.
    Если внутри неё обнаруживаются два голоса — возвращает два списка индексов.
    Иначе возвращает None.

    Алгоритм: агломеративная кластеризация с complete-linkage по косинусному расстоянию.
    Делим только на 2 кластера (добавление третьего — слишком агрессивно).
    """
    embs = np.array([embeddings[i] for i in indices], dtype=np.float64)

    # Быстрая проверка: если максимальное попарное расстояние мало — один голос
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
        "split_merged_speakers: group of %d segs has max_dist=%.3f > %.3f — splitting",
        n, max_dist, _SPLIT_DISTANCE_THRESHOLD,
    )

    # Агломеративная кластеризация sklearn (только CPU, очень быстро на N<50)
    try:
        from sklearn.cluster import AgglomerativeClustering
    except ImportError:
        log.warning("split_merged_speakers: sklearn not available, skipping split")
        return None

    labels = AgglomerativeClustering(
        n_clusters=2,
        metric="cosine",
        linkage="complete",
    ).fit_predict(embs)

    group_a = [indices[i] for i in range(n) if labels[i] == 0]
    group_b = [indices[i] for i in range(n) if labels[i] == 1]

    # Отклоняем если один из кластеров слишком маленький (< 2 сегментов)
    if len(group_a) < 2 or len(group_b) < 2:
        log.info(
            "split_merged_speakers: split rejected — unbalanced clusters (%d / %d)",
            len(group_a), len(group_b),
        )
        return None

    log.info(
        "split_merged_speakers: split accepted — cluster_a=%d segs, cluster_b=%d segs",
        len(group_a), len(group_b),
    )
    return group_a, group_b


def split_merged_speakers(
    pyannote_groups: dict[str, list[int]],
    embeddings: dict[int, np.ndarray],
) -> dict[str, list[int]]:
    """
    Принимает группировку по меткам pyannote и словарь эмбеддингов.
    Возвращает новую группировку где слипшиеся спикеры разделены.

    pyannote_groups: { "SPEAKER_01": [idx1, idx2, ...], ... }
    embeddings:      { idx: np.ndarray, ... }
    """
    result: dict[str, list[int]] = {}
    split_count = 0

    for spk_label, indices in pyannote_groups.items():
        # Фильтруем индексы у которых есть эмбеддинг
        valid = [i for i in indices if i in embeddings]

        if len(valid) < _SPLIT_MIN_SEGMENTS:
            # Слишком мало сегментов — не пытаемся делить
            result[spk_label] = indices
            continue

        split = _split_group(valid, embeddings)
        if split is None:
            result[spk_label] = indices
        else:
            group_a, group_b = split
            result[spk_label] = group_a
            result[spk_label + "_split_0"] = group_b
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

        # Сортируем по длительности: сначала длинные (longest_first внутри группы)
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