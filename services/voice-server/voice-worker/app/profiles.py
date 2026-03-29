"""In-memory speaker profiles and pending short utterances."""

from __future__ import annotations

from collections import deque
from typing import Optional

import numpy as np

from app import config


def peak_normalize(chunk: np.ndarray) -> np.ndarray:
    peak = np.max(np.abs(chunk))
    if peak < 1e-8:
        return chunk
    return chunk / peak


def cosine_sim(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.dot(a, b) / (np.linalg.norm(a) * np.linalg.norm(b) + 1e-8))


class SpeakerProfile:
    def __init__(
        self,
        voice_id: str,
        initial_embedding: np.ndarray,
        display_name: str,
    ):
        self.voice_id = voice_id
        self._display_name = display_name
        self.centroid = initial_embedding.copy()
        self.buffer: deque = deque(
            [initial_embedding.copy()],
            maxlen=config.EMBEDDING_BUFFER_SIZE,
        )
        self.segment_count = 0

    @property
    def display_name(self) -> str:
        return self._display_name

    @display_name.setter
    def display_name(self, value: str) -> None:
        self._display_name = value

    def vote_similarity(self, embedding: np.ndarray) -> float:
        if not self.buffer:
            return -1.0
        sims = [
            float(
                np.dot(e, embedding)
                / (np.linalg.norm(e) * np.linalg.norm(embedding) + 1e-8)
            )
            for e in self.buffer
        ]
        return float(np.mean(sims)) if sims else -1.0

    def update(self, embedding: np.ndarray, sim: float) -> None:
        self.buffer.append(embedding.copy())
        self.segment_count += 1
        if sim >= config.SIMILARITY_UPDATE_MIN:
            alpha = 0.05 + 0.10 * (sim - config.SIMILARITY_UPDATE_MIN)
            self.centroid = (1 - alpha) * self.centroid + alpha * embedding

    def soft_assign(self, embedding: np.ndarray) -> None:
        self.buffer.append(embedding.copy())
        self.segment_count += 1


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
