# ECAPA-TDNN — speechbrain/spkrec-ecapa-voxceleb

## Общее

| Параметр | Значение |
|---|---|
| Размер модели | ~90 MB |
| Архитектура | ECAPA-TDNN (1D свёртки + SE-блоки + attention pooling) |
| Обучена на | VoxCeleb1 + VoxCeleb2 (студийное/телефонное аудио) |
| Диапазон сходства (один человек) | 0.41–0.60 в шумных условиях |
| Диапазон сходства (разные люди) | 0.11–0.53 в шумных условиях |

## Наблюдаемая проблема

Диапазоны сходства одного и разных людей **перекрываются** при записи
одним микрофоном в комнате с несколькими людьми. Это ограничение модели —
она обучалась на чистом аудио, плохо обобщается на реверберацию и наложение голосов.

## Рекомендуемые константы

### Python

```python
SIMILARITY_THRESHOLD_CALIBRATION = 0.45   # строгий при заполнении реестра
SIMILARITY_THRESHOLD_MATCHED     = 0.42   # мягкий после заполнения
SIMILARITY_UPDATE_MIN            = 0.50   # минимум для обновления центроида
MIN_SPEAKER_DURATION             = 1.5    # сек, минимум голоса для эмбеддинга
EMBEDDING_BUFFER_SIZE            = 10     # последних эмбеддингов на спикера
```

### Go

```go
chunkDuration = 30    // сек
overlapSec    = 10    // сек
audioFilter   = "highpass=f=300,lowpass=f=3400,dynaudnorm=p=0.9"
```

## Настройка под стиль игры

### Тихая комната, хороший микрофон
```python
SIMILARITY_THRESHOLD_CALIBRATION = 0.65
SIMILARITY_THRESHOLD_MATCHED     = 0.55
SIMILARITY_UPDATE_MIN            = 0.65
```

### Шумная комната, один микрофон (текущий случай)
```python
SIMILARITY_THRESHOLD_CALIBRATION = 0.45
SIMILARITY_THRESHOLD_MATCHED     = 0.42
SIMILARITY_UPDATE_MIN            = 0.50
```

### Короткие реплики, игроки часто перебивают
```python
MIN_SPEAKER_DURATION = 0.8
```
```go
overlapSec = 15
```

### Длинные монологи, спокойная игра
```python
MIN_SPEAKER_DURATION = 2.5
EMBEDDING_BUFFER_SIZE = 20
```

### Постобработка записи (задержка не важна)
```go
chunkDuration = 120
overlapSec    = 20
```

## Признаки что пороги выставлены верно

- Сходство при регистрации нового игрока: `< 0.40`
- Каждый `SPEAKER_XX` ходит ровно в одного `Игрок_N`
- После заполнения реестра нет новых регистраций

## Признаки проблем и решения

| Симптом в логе | Причина | Решение |
|---|---|---|
| `SPEAKER_00 → Игрок_2` и `SPEAKER_01 → Игрок_2` | CALIBRATION слишком низкий | подними на 0.05 |
| Новый игрок с сходством 0.55+ | CALIBRATION слишком высокий | опусти на 0.05 |
| `недостаточно голоса` часто | MIN_SPEAKER_DURATION велик | понизь до 1.0 |
| После реестра спикеры скачут | MATCHED слишком высокий | опусти на 0.05 |

## Загрузка в коде

```python
from speechbrain.inference.speaker import SpeakerRecognition

embedding_model = SpeakerRecognition.from_hparams(
    source="speechbrain/spkrec-ecapa-voxceleb",
    savedir="/tmp/ecapa",
    run_opts={"device": DEVICE}
)

# Извлечение эмбеддинга
waveform = torch.tensor(combined).unsqueeze(0).float()
emb = embedding_model.encode_batch(waveform)
return emb.squeeze().cpu().numpy()
```
