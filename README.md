# Bad Apple!! на Яндекс Станции Мини 3 Про

Клип «Bad Apple!!» на LED-часах колонки, со звуком:

- **часы** — 4 цифры × 7 сегментов + двоеточие показывают клип: кадр делится на 4 полосы,
  каждая — на 7 областей, как цифра «8»; яркость сегмента (0–255) = насколько бела его
  область;
- **кольцо** из 16 светодиодов и **значок Алисы** сверху (6 зон) — белая вспышка на каждый
  такт и сразу чёрный;
- на **«кхиии»** (писк из двух тонов около 3 и 6 кГц, раз в такт, 1:25–1:50) — кольцо и
  значок красные, пока звучит.

Запуск и остановка — по сети (быстрые команды Siri). На время показа ночник YAM встаёт на
паузу и микрофон Алисы выключается; после — всё возвращается.

Проверено на прошивке `0.12.1.22.3114898626.20250530.factory-12`, YAM 0.1.x (октябрь 2026).

## Что нужно

- Станция Мини 3 Про (Pickle) с root, SSH и [YAM](https://hackalice.me/recipes/yam/)
  (старый YAM с плагинами `.yap`; автообновление YAM лучше выключить);
- в настройках YAM `allow_plugin_binaries = true` и модель в `data/device.toml`
  (`model = "yandexmini_3_pro"`) — как для
  [AirPlay](https://hackalice.me/devices/station-mini3-pro/#airplay);
- для подсветки кольца через YAM — профиль `midi` со ссылками на светодиоды Pickle
  ([как](https://hackalice.me/devices/station-mini3-pro/#led-pickle));
- на компьютере: [Go](https://go.dev/dl/) 1.23+, `ffmpeg`, CLI `yam`;
- **свой файл клипа** «Bad Apple!! PV (影絵)», 480×360, 30 кадров/с, 3:39. Песни и кадров
  в репозитории нет — они делаются из вашего файла.

## Сборка

### Mac / Linux

```sh
sh build.sh "Bad Apple PV.mp4"
```

Получится `out/badapple.yap` (~5 МБ): программа для колонки, кадры (`frames.bin`, 33 байта
на кадр) и песня (`audio.mp3`).

### Windows (PowerShell 7)

Нужны Go и ffmpeg: `winget install GoLang.Go`, `winget install Gyan.FFmpeg`. Из папки
репозитория:

```powershell
New-Item -ItemType Directory -Force out\badapple\bin, out\badapple\data
cd gen
go run . data "C:\путь\к\Bad Apple PV.mp4" ..\out\badapple\data
cd ..\player
$env:GOOS = "linux"; $env:GOARCH = "arm64"; $env:CGO_ENABLED = "0"
go build -trimpath -ldflags="-s -w" -o ..\out\badapple\bin\badapple .
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
cd ..
Copy-Item plugin.toml out\badapple\
tar -czf out\badapple.yap -C out\badapple plugin.toml bin/badapple data/frames.bin data/audio.mp3
```

### Превью без колонки

```sh
cd gen
go run . preview "Bad Apple PV.mp4" ../out/preview.mp4      # клип + часы + кольцо, со звуком
go run . scheme "Bad Apple PV.mp4" 47 ../out/scheme.png     # какие области кадра в какие сегменты
```

## Установка

```sh
yam install out/badapple.yap
yam enable badapple
```

**`yam service enable badapple` не делайте** — иначе YAM будет запускать показ при каждом
включении колонки (проигрыватель это отсекает: в первые 2 минуты после загрузки не
играет, но лучше не включать).

Проверка: `yam list` — в списке есть `badapple`.

## Запуск: быстрые команды Siri

В «Командах» на iPhone/Mac: «+» → действие **«Получить содержимое URL»** → «Показать ещё»
→ **Метод: POST**. `IP` — адрес колонки.

| Команда | Действия |
|---|---|
| **Bad Apple старт** | 1) `http://IP:8099/api/plugins/badapple/stop` 2) `http://IP:8099/api/plugins/badapple/service/start` |
| **Bad Apple стоп** | `http://IP:8099/api/plugins/badapple/stop` |

В «старт» сначала стоп: так показ всегда начинается заново, даже если прошлый ещё идёт.

То же из терминала:

```sh
curl -X POST http://IP:8099/api/plugins/badapple/service/start
curl -X POST http://IP:8099/api/plugins/badapple/stop
```

## Как устроено

| Где | Что |
|---|---|
| [gen/](gen) | на компьютере: кадры → яркость 30 сегментов; такты — подъёмы энергии баса (< 150 Гц); «кхиии» — список времён в коде (`screech`), найден по энергии полос 3 и 6 кГц |
| [player/](player) | на колонке: `mpg123` играет песню, кадр выбирается по времени от старта звука (30 кадров/с) и пишется в `/sys/class/leds/pickle:*` |
| [plugin.toml](plugin.toml) | плагин YAM: `kind = "binary"`, `mode = "service"` |

**Перед показом** проигрыватель: не стартует, если идёт AirPlay; останавливает ночник YAM
(если горел); выключает микрофон событием `SW_MUTE_DEVICE` в устройство `mute` (как
кнопкой); запоминает, что было на часах.
**После** (конец песни или `stop`): глушит звук, возвращает кольцо Алисе (триггер
`ledfb-ring`) и часы, включает микрофон и ночник. После конца песни программа не
выходит, а ждёт `stop` — чтобы YAM не перезапустил её. Если её убили без уборки, следующий
запуск сначала доделывает уборку (`/run/badapple/state`).

**Синхронизация.** Звук доходит до динамика позже старта `mpg123` (декодер + буфер `dmix`).
Сдвиг света — `defaultOffset` в `player/main.go` (0,20 с) или число в секундах в файле
`/data/quazar/yam/data/plugins/badapple/data/offset` на колонке. С 0,20 с свет совпадает со звуком.

## Ограничения

- Алиса перерисовывает время на часах раз в минуту — может мелькнуть на 1/30 с.
- Сегменты `H0`–`H3`, `DM`, `DEGREE` во время показа не горят: их место на панели не
  выяснено.
- 30 сегментов вместо 172 800 пикселей: узнаются заливки, смены «белый/чёрный», крупные
  фигуры, но не силуэты.
- Новый YAM (после перехода на пакеты `.yampkg`) плагины `.yap` не ставит.

## Удаление

```sh
curl -X POST http://IP:8099/api/plugins/badapple/stop
yam rm badapple
```
