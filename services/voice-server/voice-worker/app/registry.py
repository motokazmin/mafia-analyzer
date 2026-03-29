"""Global voice registry: matching, session state, persistence."""

from __future__ import annotations

import time
from typing import TYPE_CHECKING, Optional, Tuple

import numpy as np

from app import config
from app.profiles import SpeakerProfile
from app.store import VoiceStore

if TYPE_CHECKING:
    pass


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

    def reset_session(self) -> None:
        """New game: clear pending and calibration timers; keep voice profiles."""
        self.session_max_speakers = None
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
        return p

    def persist_centroid(self, profile: SpeakerProfile) -> None:
        if not config.ENABLE_VOICE_LEARNING:
            return
        self.store.update_voice(
            profile.voice_id,
            profile.centroid.astype(np.float32),
            segment_count_delta=1,
        )

    def match_or_register(self, embedding: np.ndarray) -> Tuple[SpeakerProfile, float]:
        """Возвращает профиль и лучшую косинусную близость к существующим центроидам (до решения)."""
        emb = np.asarray(embedding, dtype=np.float64)
        best_profile: Optional[SpeakerProfile] = None
        best_sim = -1.0
        for profile in self.profiles.values():
            sim = profile.vote_similarity(emb)
            if sim > best_sim:
                best_sim, best_profile = sim, profile

        calibration = self._is_calibration_phase()

        if (
            best_sim >= config.THRESHOLD_CONFIDENT_MATCH
            and best_profile is not None
        ):
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
