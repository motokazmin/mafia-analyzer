"""WhisperX + diarization + WavLM (lazy-loaded)."""

from __future__ import annotations

import gc
from typing import Any, Optional

import numpy as np
import torch
import whisperx
from whisperx.diarize import DiarizationPipeline

# Colab / разные версии transformers: WavLMForXVector может быть только через lazy-import.
try:
    from transformers import AutoFeatureExtractor, WavLMForXVector
except ImportError:  # pragma: no cover
    from transformers import AutoFeatureExtractor
    from transformers.models.wavlm.modeling_wavlm import WavLMForXVector

from app import config


class ModelHolder:
    def __init__(self) -> None:
        self.whisper_model = None
        self.diarize_model = None
        self.wavlm_model = None
        self.feature_extractor = None
        self.device = config.DEVICE
        self._loaded = False

    def ensure_loaded(self) -> None:
        if self._loaded:
            return
        if not config.HF_TOKEN:
            raise RuntimeError("HF_TOKEN is required for diarization (pyannote)")
        compute_type = "float16" if self.device == "cuda" else "float32"
        self.whisper_model = whisperx.load_model(
            config.WHISPER_MODEL,
            self.device,
            compute_type=compute_type,
            vad_options={"vad_onset": 0.5, "vad_offset": 0.363},
        )
        self.diarize_model = DiarizationPipeline(
            token=config.HF_TOKEN, device=self.device
        )
        mid = "microsoft/wavlm-base-plus-sv"
        self.feature_extractor = AutoFeatureExtractor.from_pretrained(mid)
        self.wavlm_model = WavLMForXVector.from_pretrained(mid).to(self.device)
        self.wavlm_model.eval()
        self._loaded = True

    def unload_cuda(self) -> None:
        gc.collect()
        if torch.cuda.is_available():
            torch.cuda.empty_cache()

    def run_wavlm(self, combined: np.ndarray, sample_rate: int) -> Optional[np.ndarray]:
        self.ensure_loaded()
        try:
            inputs = self.feature_extractor(
                combined, sampling_rate=sample_rate, return_tensors="pt", padding=True
            )
            inputs = {k: v.to(self.device) for k, v in inputs.items()}
            with torch.no_grad():
                emb = self.wavlm_model(**inputs).embeddings
            return emb.squeeze().cpu().numpy()
        except Exception as e:
            print(f">>> WavLM error: {e}")
            return None

    def transcribe_align(
        self, audio: np.ndarray
    ) -> tuple[dict[str, Any], np.ndarray]:
        self.ensure_loaded()
        result = self.whisper_model.transcribe(
            audio, batch_size=16, language="ru"
        )
        model_a, meta = whisperx.load_align_model(
            language_code=result["language"], device=self.device
        )
        result = whisperx.align(
            result["segments"], model_a, meta, audio, self.device
        )
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
        kwargs = {}
        if min_speakers is not None:
            kwargs["min_speakers"] = min_speakers
        if max_speakers is not None:
            kwargs["max_speakers"] = max_speakers
        try:
            return self.diarize_model(audio, **kwargs)
        except Exception as e:
            if "n_samples" in str(e) and "n_clusters" in str(e):
                fb = {}
                if max_speakers is not None:
                    fb["max_speakers"] = max_speakers
                return self.diarize_model(audio, **fb)
            raise


holder = ModelHolder()
