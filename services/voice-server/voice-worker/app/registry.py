"""Global voice registry: matching, session state, persistence."""

from __future__ import annotations

import logging
import time
from dataclasses import dataclass
from typing import TYPE_CHECKING, Optional, Tuple

import numpy as np

from app import config
from app.profiles import SpeakerProfile
from app.store import VoiceStore

if TYPE_CHECKING:
    pass

log = logging.getLogger("voice")

# Minimum stored segment embeddings before we attempt split analysis
_SPLIT_ANALYSIS_MIN_EMBEDDINGS = 10
# Intra-profile dispersion threshold: mean pairwise cosine distance to flag
_SPLIT_DISPERSION_THRESHOLD = float(
    getattr(config, "SPLIT_DISTANCE_THRESHOLD", 0.18)
)


@dataclass
class SplitCandidate:
    voice_id: str
    display_name: str
    embedding_count: int
    max_pairwise_dist: float
    # cluster_a / cluster_b: indices into the stored embeddings list
    cluster_a: list[int]
    cluster_b: list[int]


class VoiceRegistry:
    def __init__(self, store: VoiceStore) -> None:
        self.store = store
        self.profiles: dict[str, SpeakerProfile] = {}
        self.session_max_speakers: Optional[int] = None
        self.session_start_time: float = time.time()
        self.pending_pool: list = []
        self.load_from_store()

    def load_from_store(self) -> None:
        self.profiles.clear()
        for row in self.store.list_voices():
            label = row.display_name or f"voice_{row.voice_id[:8]}"
            self.profiles[row.voice_id] = SpeakerProfile(
                row.voice_id,
                row.centroid.astype(np.float64),
                label,
            )
            self.profiles[row.voice_id].segment_count = row.segment_count

        persisted = self.store.load_session_max_speakers()
        if persisted is not None and self.session_max_speakers is None:
            self.session_max_speakers = persisted
            log.info("registry: restored session_max_speakers=%d from store", persisted)

    def reset_session(self) -> None:
        """New game: clear pending and calibration timers; keep voice profiles."""
        self.session_max_speakers = None
        self.store.save_session_max_speakers(None)
        self.session_start_time = time.time()
        self.pending_pool = []

    def _is_calibration_phase(self) -> bool:
        elapsed = time.time() - self.session_start_time
        if elapsed < config.CALIBRATION_WINDOW:
            return True
        if self.session_max_speakers is None:
            return True
        return len(self.profiles) < self.session_max_speakers

    def _extra_slots_available(self) -> bool:
        if self.session_max_speakers is None:
            return True
        return len(self.profiles) < self.session_max_speakers + config.MAX_EXTRA_SLOTS

    def register_new(self, embedding: np.ndarray) -> SpeakerProfile:
        n = self.store.increment_speaker_counter()
        label = f"Игрок_{n}"
        vid = self.store.insert_voice(embedding, display_name=label)
        emb = np.asarray(embedding, dtype=np.float64)
        p = SpeakerProfile(vid, emb, label)
        p.segment_count = 1
        self.profiles[vid] = p
        log.info("registry: new profile %s (%s), total profiles=%d", label, vid[:8], len(self.profiles))
        return p

    def persist_centroid(self, profile: SpeakerProfile) -> None:
        if not config.ENABLE_VOICE_LEARNING:
            return
        self.store.update_voice(
            profile.voice_id,
            profile.centroid.astype(np.float32),
            segment_count_delta=1,
        )

    def set_session_max_speakers(self, value: int) -> None:
        self.session_max_speakers = value
        self.store.save_session_max_speakers(value)

    def match_or_register(self, embedding: np.ndarray) -> Tuple[SpeakerProfile, float]:
        emb = np.asarray(embedding, dtype=np.float64)
        best_profile: Optional[SpeakerProfile] = None
        best_sim = -1.0
        for profile in self.profiles.values():
            sim = profile.vote_similarity(emb)
            if sim > best_sim:
                best_sim, best_profile = sim, profile

        calibration = self._is_calibration_phase()

        if best_sim >= config.THRESHOLD_CONFIDENT_MATCH and best_profile is not None:
            best_profile.update(emb, best_sim)
            self.persist_centroid(best_profile)
            return best_profile, float(best_sim)

        if config.THRESHOLD_SOFT_MATCH <= best_sim < config.THRESHOLD_CONFIDENT_MATCH:
            if calibration:
                p = self.register_new(emb)
                return p, float(best_sim)
            assert best_profile is not None
            best_profile.soft_assign(emb)
            return best_profile, float(best_sim)

        if best_sim < config.THRESHOLD_FORCE_NEW:
            if self._extra_slots_available():
                p = self.register_new(emb)
                return p, float(best_sim)
            assert best_profile is not None
            best_profile.soft_assign(emb)
            return best_profile, float(best_sim)

        if calibration:
            p = self.register_new(emb)
            return p, float(best_sim)
        assert best_profile is not None
        best_profile.soft_assign(emb)
        return best_profile, float(best_sim)

    def set_display_name(self, voice_id: str, name: str) -> bool:
        if not self.store.set_display_name(voice_id, name):
            return False
        if voice_id in self.profiles:
            self.profiles[voice_id].display_name = name
        return True

    # ── Split candidate detection ──────────────────────────────────────────

    def check_split_candidates(self) -> list[SplitCandidate]:
        """
        Analyse all profiles with enough stored embeddings.
        Returns SplitCandidate list for profiles whose embedding distribution
        appears bimodal (two voices merged into one).
        """
        try:
            from sklearn.cluster import AgglomerativeClustering
        except ImportError:
            log.warning("check_split_candidates: sklearn not available")
            return []

        candidates: list[SplitCandidate] = []

        for voice_id, profile in list(self.profiles.items()):
            embs = self.store.get_segment_embeddings(voice_id)
            if len(embs) < _SPLIT_ANALYSIS_MIN_EMBEDDINGS:
                continue

            mat = np.array(embs, dtype=np.float64)
            n = len(mat)

            # Max pairwise cosine distance (sample up to 50 pairs for speed)
            max_dist = 0.0
            pairs_checked = 0
            for i in range(n):
                for j in range(i + 1, n):
                    d = 1.0 - float(
                        np.dot(mat[i], mat[j])
                        / (np.linalg.norm(mat[i]) * np.linalg.norm(mat[j]) + 1e-8)
                    )
                    if d > max_dist:
                        max_dist = d
                    pairs_checked += 1
                    if pairs_checked >= 200:
                        break
                else:
                    continue
                break

            if max_dist < _SPLIT_DISPERSION_THRESHOLD:
                continue

            log.info(
                "check_split_candidates: %s (%s) max_dist=%.3f — running clustering",
                profile.display_name, voice_id[:8], max_dist,
            )

            labels = AgglomerativeClustering(
                n_clusters=2,
                metric="cosine",
                linkage="complete",
            ).fit_predict(mat)

            cluster_a = [i for i, l in enumerate(labels) if l == 0]
            cluster_b = [i for i, l in enumerate(labels) if l == 1]

            if len(cluster_a) < 2 or len(cluster_b) < 2:
                log.info(
                    "check_split_candidates: %s rejected — unbalanced (%d/%d)",
                    profile.display_name, len(cluster_a), len(cluster_b),
                )
                continue

            candidates.append(SplitCandidate(
                voice_id=voice_id,
                display_name=profile.display_name,
                embedding_count=n,
                max_pairwise_dist=round(max_dist, 4),
                cluster_a=cluster_a,
                cluster_b=cluster_b,
            ))
            log.info(
                "check_split_candidates: %s FLAGGED — %d embs, max_dist=%.3f, clusters %d/%d",
                profile.display_name, n, max_dist, len(cluster_a), len(cluster_b),
            )

        return candidates

    def split_voice(
        self,
        voice_id: str,
        cluster_a: list[int],
        cluster_b: list[int],
    ) -> Optional[Tuple[SpeakerProfile, SpeakerProfile]]:
        """
        Split voice_id into two profiles based on pre-computed cluster indices
        (indices into the stored segment_embeddings, oldest-first order).
        Returns (kept_profile, new_profile) or None on failure.
        """
        embs = self.store.get_segment_embeddings(voice_id)
        if not embs:
            log.warning("split_voice: no embeddings for %s", voice_id)
            return None

        original = self.profiles.get(voice_id)
        if original is None:
            log.warning("split_voice: voice_id %s not in profiles", voice_id)
            return None

        def _centroid(indices: list[int]) -> np.ndarray:
            vecs = np.array([embs[i] for i in indices if i < len(embs)], dtype=np.float64)
            if len(vecs) == 0:
                return original.centroid.copy()
            c = vecs.mean(axis=0)
            n = float(np.linalg.norm(c))
            return c / n if n > 1e-8 else c

        centroid_a = _centroid(cluster_a)
        centroid_b = _centroid(cluster_b)

        # Profile A: update existing voice in-place
        self.store.update_voice(
            voice_id,
            np.asarray(centroid_a, dtype=np.float32),
            segment_count_delta=0,
        )
        original.centroid = centroid_a
        original.segment_count = len(cluster_a)

        # Profile B: new voice
        n_counter = self.store.increment_speaker_counter()
        new_name = f"{original.display_name}_2"
        new_id = self.store.insert_voice(centroid_b, display_name=new_name)
        new_profile = SpeakerProfile(new_id, centroid_b, new_name)
        new_profile.segment_count = len(cluster_b)
        self.profiles[new_id] = new_profile

        # Reassign embeddings
        self.store.split_segment_embeddings(voice_id, new_id, cluster_b)

        log.info(
            "split_voice: %s split into '%s' (%d segs) + '%s' (%d segs)",
            voice_id[:8], original.display_name, len(cluster_a),
            new_name, len(cluster_b),
        )
        return original, new_profile