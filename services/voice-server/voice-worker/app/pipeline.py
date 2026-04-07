"""WhisperX + diarization + WavLM + WeSpeaker (lazy-loaded)."""

from __future__ import annotations

import gc
import logging
import time
from typing import Any, Optional

import numpy as np
import torch
import whisperx
from whisperx.diarize import DiarizationPipeline

try:
    from transformers import AutoFeatureExtractor, WavLMForXVector
except ImportError:
    from transformers import AutoFeatureExtractor
    from transformers.models.wavlm.modeling_wavlm import WavLMForXVector

from pyannote.audio import Model as PyannoteModel
from pyannote.audio import Inference

from app import config

log = logging.getLogger("voice")

_WAVLM_MODEL = "microsoft/wavlm-base-plus-sv"
_WESPEAKER_MODEL = "pyannote/wespeaker-voxceleb-resnet34-LM"

# Переключатель: "wavlm" или "wespeaker"
_EMBEDDING_BACKEND = __import__("os").environ.get("EMBEDDING_BACKEND", "wespeaker").lower()


class ModelHolder:
    def __init__(self) -> None:
        self.whisper_model = None
        self.diarize_model = None
        self.wavlm_model = None
        self.feature_extractor = None
        self.wespeaker_inference = None
        self.device = config.DEVICE
        self._loaded = False

    def ensure_loaded(self) -> None:
        if self._loaded:
            return

        if not config.HF_TOKEN:
            raise RuntimeError("HF_TOKEN is required for diarization (pyannote)")

        compute_type = "float16" if self.device == "cuda" else "int8"

        config.THRESHOLD_CONFIDENT_MATCH = 0.55
        config.THRESHOLD_SOFT_MATCH      = 0.40
        config.THRESHOLD_FORCE_NEW       = 0.25
        config.SIMILARITY_UPDATE_MIN     = 0.45
        config.PENDING_MATCH_THRESHOLD   = 0.35



        log.info("=" * 60)
        log.info("loading models — startup config:")
        log.info("  device           : %s", self.device.upper())
        log.info("  compute_type     : %s", compute_type)
        log.info("  whisper model    : %s", config.WHISPER_MODEL)
        log.info("  embedding backend: %s", _EMBEDDING_BACKEND)
        if _EMBEDDING_BACKEND == "wavlm":
            log.info("  wavlm model      : %s", _WAVLM_MODEL)
        else:
            log.info("  wespeaker model  : %s", _WESPEAKER_MODEL)
        log.info("  language         : ru (fixed)")
        log.info("  vad_onset        : 0.500  vad_offset: 0.363")
        log.info("thresholds:")
        log.info("  confident     : %.2f", config.THRESHOLD_CONFIDENT_MATCH)
        log.info("  soft_match    : %.2f", config.THRESHOLD_SOFT_MATCH)
        log.info("  force_new     : %.2f", config.THRESHOLD_FORCE_NEW)
        log.info("  sim_update_min: %.2f", config.SIMILARITY_UPDATE_MIN)
        log.info("  pending_match : %.2f", config.PENDING_MATCH_THRESHOLD)
        log.info("session:")
        log.info("  calibration_window : %.0fs", config.CALIBRATION_WINDOW)
        log.info("  max_extra_slots    : %d",   config.MAX_EXTRA_SLOTS)
        log.info("  min_speaker_dur    : %.1fs", config.MIN_SPEAKER_DURATION)
        log.info("  embedding_buf_size : %d",   config.EMBEDDING_BUFFER_SIZE)
        log.info("  voice_learning     : %s",   config.ENABLE_VOICE_LEARNING)
        log.info("  split_distance_threshold : %.3f", config.SPLIT_DISTANCE_THRESHOLD)
        log.info("  db_path            : %s",   config.DATABASE_PATH)
        log.info("=" * 60)

        t0 = time.monotonic()
        log.info("[1/3] loading Whisper %s ...", config.WHISPER_MODEL)
        self.whisper_model = whisperx.load_model(
            config.WHISPER_MODEL,
            self.device,
            compute_type=compute_type,
            vad_options={"vad_onset": 0.500, "vad_offset": 0.363},
        )
        log.info("[1/3] Whisper loaded in %.1fs", time.monotonic() - t0)

        t1 = time.monotonic()
        log.info("[2/3] loading pyannote DiarizationPipeline ...")
        self.diarize_model = DiarizationPipeline(
            token=config.HF_TOKEN,
            device=self.device,
        )
        log.info("[2/3] pyannote loaded in %.1fs", time.monotonic() - t1)

        t2 = time.monotonic()
        if _EMBEDDING_BACKEND == "wavlm":
            log.info("[3/3] loading WavLM %s ...", _WAVLM_MODEL)
            self.feature_extractor = AutoFeatureExtractor.from_pretrained(_WAVLM_MODEL)
            self.wavlm_model = WavLMForXVector.from_pretrained(_WAVLM_MODEL).to(self.device)
            self.wavlm_model.eval()
            log.info("[3/3] WavLM loaded in %.1fs", time.monotonic() - t2)
        else:
            log.info("[3/3] loading WeSpeaker %s ...", _WESPEAKER_MODEL)
            _ws_model = PyannoteModel.from_pretrained(
                _WESPEAKER_MODEL,
                use_auth_token=config.HF_TOKEN,
            )
            self.wespeaker_inference = Inference(
                _ws_model,
                window="whole",
            )
            self.wespeaker_inference.to(torch.device(self.device))
            log.info("[3/3] WeSpeaker loaded in %.1fs", time.monotonic() - t2)

        total = time.monotonic() - t0
        log.info("all models ready in %.1fs", total)
        log.info("=" * 60)

        self._loaded = True

    def unload_cuda(self) -> None:
        if self.device == "cuda":
            torch.cuda.empty_cache()
        gc.collect()

    def run_wavlm(self, combined: np.ndarray, sample_rate: int) -> Optional[np.ndarray]:
        """Извлечение эмбеддинга через активный бэкенд (wavlm или wespeaker)."""
        self.ensure_loaded()
        if _EMBEDDING_BACKEND == "wespeaker":
            return self._run_wespeaker(combined, sample_rate)
        return self._run_wavlm_internal(combined, sample_rate)

    def _run_wavlm_internal(self, combined: np.ndarray, sample_rate: int) -> Optional[np.ndarray]:
        try:
            inputs = self.feature_extractor(
                combined, sampling_rate=sample_rate, return_tensors="pt", padding=True
            )
            inputs = {k: v.to(self.device) for k, v in inputs.items()}
            with torch.no_grad():
                emb = self.wavlm_model(**inputs).embeddings
            return emb.squeeze().cpu().numpy()
        except Exception as e:
            log.error("WavLM error: %s", e)
            return None

    def _run_wespeaker(self, combined: np.ndarray, sample_rate: int) -> Optional[np.ndarray]:
        try:
            waveform = torch.tensor(combined, dtype=torch.float32).unsqueeze(0)
            sample_dict = {"waveform": waveform, "sample_rate": sample_rate}
            emb = self.wespeaker_inference(sample_dict)
            return np.array(emb)
        except Exception as e:
            log.error("WeSpeaker error: %s", e)
            return None

    def transcribe_align(self, audio: np.ndarray) -> tuple[dict[str, Any], np.ndarray]:
        self.ensure_loaded()
        b_size = 16 if self.device == "cuda" else 4
        audio_sec = len(audio) / 16000

        log.info("transcribe: audio=%.1fs, batch_size=%d, lang=ru", audio_sec, b_size)
        t0 = time.monotonic()
        result = self.whisper_model.transcribe(audio, batch_size=b_size, language="ru")
        log.info("transcribe done in %.1fs — %d raw segment(s)", time.monotonic() - t0, len(result.get("segments", [])))

        t1 = time.monotonic()
        model_a, meta = whisperx.load_align_model(
            language_code=result["language"], device=self.device
        )
        result = whisperx.align(result["segments"], model_a, meta, audio, self.device)
        log.info("align done in %.1fs — %d aligned segment(s)", time.monotonic() - t1, len(result.get("segments", [])))

        del model_a
        self.unload_cuda()
        return result, audio

    def diarize(
        self,
        audio: np.ndarray,
        min_speakers: Optional[int] = None,
        max_speakers: Optional[int] = None,
    ) -> Any:
        self.ensure_loaded()
        audio_sec = len(audio) / 16000
        kwargs: dict[str, Any] = {}
        if min_speakers is not None:
            kwargs["min_speakers"] = min_speakers
        if max_speakers is not None:
            kwargs["max_speakers"] = max_speakers

        log.info(
            "diarize: audio=%.1fs, min_speakers=%s, max_speakers=%s",
            audio_sec,
            kwargs.get("min_speakers", "auto"),
            kwargs.get("max_speakers", "auto"),
        )
        t0 = time.monotonic()
        try:
            result = self.diarize_model(audio, **kwargs)
            log.info("diarize done in %.1fs", time.monotonic() - t0)
            return result
        except Exception as e:
            if "n_samples" in str(e) and "n_clusters" in str(e):
                log.warning("diarize failed with n_clusters error, retrying without min_speakers: %s", e)
                fb: dict[str, Any] = {}
                if max_speakers is not None:
                    fb["max_speakers"] = max_speakers
                result = self.diarize_model(audio, **fb)
                log.info("diarize retry done in %.1fs", time.monotonic() - t0)
                return result
            raise


holder = ModelHolder()