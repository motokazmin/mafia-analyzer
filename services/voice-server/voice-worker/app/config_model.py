"""Pydantic-схема настроек voice-worker (единый источник полей)."""

from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, ConfigDict, Field


class VoiceServerConfig(BaseModel):
    """Все настройки сервиса. Значения приходят из YAML и/или переменных окружения."""

    model_config = ConfigDict(extra="ignore", validate_assignment=True)

    # API
    api_key: str = Field(default="barchik", description="VOICE_SERVER_API_KEY")
    max_file_size_mb: int = Field(default=500, ge=1, description="VOICE_SERVER_MAX_FILE_MB")

    # Paths
    db_path: Optional[str] = Field(default=None, description="VOICE_SERVER_DB")
    use_google_drive: bool = Field(default=False, description="VOICE_SERVER_USE_GOOGLE_DRIVE")

    # Runtime
    device: Optional[str] = Field(
        default=None,
        description="VOICE_SERVER_DEVICE; пусто = авто (CUDA если доступен)",
    )

    # Models
    whisper_model: str = Field(default="large-v2", description="VOICE_SERVER_WHISPER_MODEL")
    hf_token: str = Field(default="", description="HF_TOKEN")

    enable_voice_learning: bool = Field(default=True, description="ENABLE_VOICE_LEARNING")

    voice_threshold_preset: str = Field(
        default="balanced",
        description="VOICE_THRESHOLD_PRESET (balanced|strict|loose; иначе как balanced)",
    )

    threshold_confident_match: float = Field(default=0.75, description="THRESHOLD_CONFIDENT_MATCH")
    threshold_soft_match: float = Field(default=0.60, description="THRESHOLD_SOFT_MATCH")
    threshold_force_new: float = Field(default=0.45, description="THRESHOLD_FORCE_NEW")
    similarity_update_min: float = Field(default=0.65, description="SIMILARITY_UPDATE_MIN")
    pending_match_threshold: float = Field(default=0.55, description="PENDING_MATCH_THRESHOLD")
    split_distance_threshold: float = Field(default=0.22, description="SPLIT_DISTANCE_THRESHOLD")

    calibration_window: float = Field(default=300.0, description="CALIBRATION_WINDOW_SEC")
    max_extra_slots: int = Field(default=2, ge=0, description="MAX_EXTRA_SLOTS")
    min_speaker_duration: float = Field(default=1.0, ge=0, description="MIN_SPEAKER_DURATION")
    pending_max_chunks: int = Field(default=3, ge=1, description="PENDING_MAX_CHUNKS")
    embedding_buffer_size: int = Field(default=10, ge=1, description="EMBEDDING_BUFFER_SIZE")

    min_long_segment_sec: float = Field(default=2.0, ge=0, description="MIN_LONG_SEGMENT_SEC")

    embedding_backend: str = Field(default="wespeaker", description="EMBEDDING_BACKEND")

    hdbscan_min_cluster_size: int = Field(default=3, ge=1, description="HDBSCAN_MIN_CLUSTER_SIZE")
    split_max_clusters: int = Field(default=4, ge=1, description="SPLIT_MAX_CLUSTERS")
    sub_rebuild_every: int = Field(default=10, ge=1, description="SUB_REBUILD_EVERY")

    segment_embeddings_max: int = Field(default=100, ge=1, description="SEGMENT_EMBEDDINGS_MAX")
    max_sub_centroids: int = Field(default=6, ge=1, description="MAX_SUB_CENTROIDS")
    sub_min_cluster_size: int = Field(default=3, ge=1, description="SUB_MIN_CLUSTER_SIZE")
