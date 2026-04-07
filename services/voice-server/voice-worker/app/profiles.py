"""In-memory speaker profiles and pending short utterances."""

from __future__ import annotations

import logging
from collections import deque
from typing import Optional

import numpy as np

from app import config

log = logging.getLogger("voice")

# Максимальное число суб-центроидов на профиль
_MAX_SUB_CENTROIDS = config.MAX_SUB_CENTROIDS
# Минимум эмбеддингов в кластере чтобы создать суб-центроид
_SUB_MIN_CLUSTER_SIZE = config.SUB_MIN_CLUSTER_SIZE


def peak_normalize(chunk: np.ndarray) -> np.ndarray:
    peak = np.max(np.abs(chunk))
    if peak < 1e-8:
        return chunk
    return chunk / peak


def cosine_sim(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b) + 1e-8))


def _normalize(v: np.ndarray) -> np.ndarray:
    n = float(np.linalg.norm(v))
    return v / n if n > 1e-8 else v


class SpeakerProfile:
    def __init__(
        self,
        voice_id: str,
        initial_embedding: np.ndarray,
        display_name: str,
    ):
        self.voice_id = voice_id
        self._display_name = display_name
        self.centroid = _normalize(initial_embedding.copy().astype(np.float64))
        self.buffer: deque = deque(
            [initial_embedding.copy()],
            maxlen=config.EMBEDDING_BUFFER_SIZE,
        )
        self.segment_count = 0

        # Мультидиполь: список суб-центроидов (нормализованные векторы).
        # Пустой список = режим совместимости, используется только centroid.
        self.sub_centroids: list[np.ndarray] = []

    @property
    def display_name(self) -> str:
        return self._display_name

    @display_name.setter
    def display_name(self, value: str) -> None:
        self._display_name = value

    # ── Схожесть ──────────────────────────────────────────────────────────

    def vote_similarity(self, embedding: np.ndarray) -> float:
        """
        Если суб-центроиды есть — возвращает max cosine similarity по ним.
        Иначе — взвешенное среднее по buffer (старое поведение).
        """
        emb = np.asarray(embedding, dtype=np.float64)

        if self.sub_centroids:
            best = max(
                float(np.dot(emb, sc) / (np.linalg.norm(emb) * np.linalg.norm(sc) + 1e-8))
                for sc in self.sub_centroids
            )
            return best

        # Fallback: буферное голосование (как раньше)
        if not self.buffer:
            return -1.0
        buf = list(self.buffer)
        n = len(buf)
        weights = np.exp(np.linspace(-1.0, 0.0, n))
        weights /= weights.sum()
        sims = np.array([
            float(np.dot(e, emb) / (np.linalg.norm(e) * np.linalg.norm(emb) + 1e-8))
            for e in buf
        ])
        return float(np.dot(weights, sims))

    def best_sub_index(self, embedding: np.ndarray) -> int:
        """Возвращает индекс ближайшего суб-центроида (или 0 если их нет)."""
        if not self.sub_centroids:
            return 0
        emb = np.asarray(embedding, dtype=np.float64)
        sims = [
            float(np.dot(emb, sc) / (np.linalg.norm(emb) * np.linalg.norm(sc) + 1e-8))
            for sc in self.sub_centroids
        ]
        return int(np.argmax(sims))

    # ── Обновление ────────────────────────────────────────────────────────

    def update(self, embedding: np.ndarray, sim: float) -> None:
        emb = np.asarray(embedding, dtype=np.float64)
        self.buffer.append(emb.copy())
        self.segment_count += 1

        if sim >= config.SIMILARITY_UPDATE_MIN:
            alpha = 0.03  # фиксированный медленный дрейф (было 0.05 + 0.10*(sim-min))
            new_centroid = (1 - alpha) * self.centroid + alpha * emb
            self.centroid = _normalize(new_centroid)

            # Обновляем ближайший суб-центроид (если есть)
            if self.sub_centroids:
                idx = self.best_sub_index(emb)
                new_sub = (1 - alpha) * self.sub_centroids[idx] + alpha * emb
                self.sub_centroids[idx] = _normalize(new_sub)

    def soft_assign(self, embedding: np.ndarray) -> None:
        emb = np.asarray(embedding, dtype=np.float64)
        self.buffer.append(emb.copy())
        self.segment_count += 1

    # ── Построение суб-центроидов ─────────────────────────────────────────

    def rebuild_sub_centroids(self, embeddings: list[np.ndarray]) -> bool:
        """
        Запускает HDBSCAN по переданным эмбеддингам и строит суб-центроиды.
        Возвращает True если суб-центроиды успешно построены.
        Минимум _SUB_MIN_CLUSTER_SIZE * 2 точек для запуска.
        """
        if len(embeddings) < _SUB_MIN_CLUSTER_SIZE * 2:
            return False

        try:
            import hdbscan as hdbscan_lib
            from sklearn.metrics.pairwise import cosine_distances
        except ImportError:
            log.warning("rebuild_sub_centroids: hdbscan/sklearn не установлены")
            return False

        mat = np.array(embeddings, dtype=np.float64)
        dist_mat = cosine_distances(mat).astype(np.float64)

        labels = hdbscan_lib.HDBSCAN(
            min_cluster_size=_SUB_MIN_CLUSTER_SIZE,
            metric="precomputed",
            cluster_selection_method="eom",
        ).fit_predict(dist_mat)

        unique = sorted(set(labels) - {-1})
        if not unique:
            # Один общий кластер — один суб-центроид из среднего
            sub = _normalize(mat.mean(axis=0))
            self.sub_centroids = [sub]
            log.info(
                "rebuild_sub_centroids [%s]: 1 cluster (все точки) из %d эмб",
                self.display_name, len(embeddings),
            )
            return True

        if len(unique) > _MAX_SUB_CENTROIDS:
            log.warning(
                "rebuild_sub_centroids [%s]: %d кластеров > MAX %d, обрезаем",
                self.display_name, len(unique), _MAX_SUB_CENTROIDS,
            )
            unique = unique[:_MAX_SUB_CENTROIDS]

        subs = []
        for cid in unique:
            cluster_vecs = mat[[i for i, l in enumerate(labels) if l == cid]]
            if len(cluster_vecs) < 2:
                continue
            subs.append(_normalize(cluster_vecs.mean(axis=0)))

        if not subs:
            return False

        self.sub_centroids = subs
        noise = list(labels).count(-1)
        log.info(
            "rebuild_sub_centroids [%s]: %d суб-центроидов из %d эмб (noise=%d)",
            self.display_name, len(subs), len(embeddings), noise,
        )
        return True


class PendingEntry:
    def __init__(
        self,
        chunks: list,
        duration: float,
        rough_emb: Optional[np.ndarray],
    ):
        self.chunks = list(chunks)
        self.total_duration = duration
        self.rough_emb = rough_emb
        self.chunks_seen = 1

    def add(
        self,
        chunks: list,
        duration: float,
        rough_emb: Optional[np.ndarray],
    ) -> None:
        self.chunks.extend(chunks)
        self.total_duration += duration
        self.chunks_seen += 1
        if rough_emb is not None:
            self.rough_emb = rough_emb

    def tick(self) -> None:
        self.chunks_seen += 1

    def is_ready(self) -> bool:
        return self.total_duration >= config.MIN_SPEAKER_DURATION

    def is_stale(self) -> bool:
        return self.chunks_seen >= config.PENDING_MAX_CHUNKS

    def get_combined(self) -> np.ndarray:
        return peak_normalize(np.concatenate(self.chunks))