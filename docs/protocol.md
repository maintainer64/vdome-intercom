# Исследование VDome

Источник: официальный RuStore, пакет `ru.mts.vdome.resident`, версия `3.0.4r`,
versionCode `300004000`, appId `2063543112`, versionId `2064884648`.
Загрузка 7 ноября 2026 через публичный RuStore API с `ruStoreVerCode: 247`.
SHA-256 загруженного APK:
`2e418c25bdfee9af1c1b562f4a53378fe80a3a9acb0ec91cdd17fa7fb5d91e58`.
APK и декомпиляция находятся в игнорируемой Git папке `research/`.

`ipatool 2.7.0` получен из официального GitHub release (последний релиз по
GitHub API на момент проверки). SHA-256 архива macOS arm64:
`5ae100d4520cf4b3f4374239e5925a9ad9a8dc02abc16bae27479e7ae99aca33`.
Apple не выдал сессию: 204/403 и затем 302 без Location. IPA не загружен;
выводы о хранении авторизации iOS делать нельзя. Анализ Android согласован
пользователем как альтернативный путь.

JADX 1.5.6 сохранил исходники с частичными ошибками в 504 методах, в том числе
корутинах и SDK. Retrofit-интерфейсы, модели и исследованные методы читаются.
Декомпиляция не считается полностью восстановленным исходным проектом.

## Подтверждено статическим анализом

| Часть | Наблюдение | Доказательство в `research/decompiled/sources/` |
| --- | --- | --- |
| REST gateway | `https://gateway.vdome.mts.ru/` | `com/ub/smartresident/BuildConfig.java`, DI `RestModule*.java` |
| Resident API | `https://resident-rest.vdome.mts.ru` | тот же BuildConfig и `AuthModule.java` |
| WebSocket fallback | `https://webrtc.domofon.mts.ru/ws` | BuildConfig; Go использует эквивалент WSS |
| SSO client | `VDome` | `ru/mts/resident/auth/BuildConfig.java` |
| Callback | gateway `/user-service/api/auth/callback` | тот же файл |
| OAuth | `/amserver/oauth2/authorize`, code flow, `device_id` | `ru/mts/sso/internal/t0.java` |
| Scopes | account, address, sso, profile, ssom, openid, phone | `ru/mts/resident/auth/di/AuthProvider3.java`, `AuthScope.java` |
| Токены SSO | `keycloakTokenPair.accessToken`, `.refreshToken`, `webSsoAccessToken`, `idToken` | `ru/mts/resident/auth/interactor/Authorize3.java` |
| Защита токенов | EncryptedSharedPreferences, AES256_SIV для ключей, AES256_GCM для значений | `com/ub/smartresident/repositories/LocalDataRepository.java` |
| Ключи хранения | AUTH_TOKEN, REFRESH_TOKEN, SSO_TOKEN, ID_TOKEN; миграция plaintext в crypto | тот же файл |
| Заголовки | Authorization: Bearer; X-Device-Type: ANDROID; Request-Id: UUID; опциональный X-Device-Token | `ru/mts/vdome/core/backend/interceptors/*` |
| Auto-open | локальные SharedPreferences `autoOpen`, `autoOpenChecked`, `autoOpenCheckedTime` | `com/ub/smartresident/call/interactor/ShowIncomingCallNotification2.java` |
| Live call | SIP REGISTER с MD5 Digest; входящий INVITE, WebSocket subprotocol `sip` | `ru/mts/callibry/signal/sip/`, `transport/TransportWebsocket.java` |
| SIP transport | Via WSS/WS и Contact transport=wss/ws следуют схеме wsUri | `com/mtsiot/intercom_callibry/Settings.java`, `ru/mts/callibry/signal/sip/Dialog.java`, `message/headers/Contact.java` |
| SIP credentials | /self → list[0].auth[0].credential: sip, secret, domain | `.../intercom/repo/mediator/repository/IntercomRepository3.java` |
| Параметры вызова | caller, call session, camera, timestamp, WSS и SIP server в push | `.../call/interactor/remote/GetRemoteCallActions3.java`, `SetupIncomingCallConnection2.java` |

SSO SDK перехватывает переход на callback и убирает `idt_token` из URL перед
запросом продуктового callback (`ru/mts/sso/internal/fc.java`). Обычный браузер
этого перехватчика не имеет. Пока сделан ручной импорт callback JSON; весь
Пользователь подтвердил вход через такой JSON и получение списка подъездов.

`device_id` в этом SDK — **стандартный Base64 от JSON**, а не голый UUID:
поля `uuid`, `platform`, `vendor`, `model`, `name`, `ip`, `timezone`, `locale`,
`useragent`, `version`, `screenSize`, `systemVersion`. Это восстановлено через
fallback-декомпиляцию `ru/mts/sso/internal/k2.java` и анализ `g2`, `d2`, `f2`,
`i4`; используется Base64.NO_WRAP. В Go сохранена эта схема с неперсональными
описаниями клиента. Тест ссылки декодирует descriptor и проверяет поля.

## Методы, использованные адаптером

| Запрос | Host | Назначение |
| --- | --- | --- |
| POST `/user-service/api/v2/auth/init` | resident-rest | прежний SMS: `{phone}` |
| POST `/user-service/api/v2/auth/login` | resident-rest | прежний SMS: `{phone,code}`; `result.accessToken/refreshToken` |
| PUT `/user-service/api/v2/auth/refresh` | gateway | `{refreshToken}`, ротация пары accessToken/refreshToken |
| GET `/domofon/api/security/v1/users/self` | gateway | подтверждение субъекта и получение SIP credentials |
| GET `/domofon/api/intercom/v2/intercoms?limit=1000&category=intercom` | gateway | список домофонов |
| GET тот же путь с `category=barrier` | gateway | шлагбаумы/другие входы |
| GET `/domofon/api/intercom/v1/cameras?limit=1000` | gateway | список камер |
| POST `/vdome-smart-facade/devices/api/v1/intercoms/{intercomId}/lock` | gateway | `{action:"open"}` |
| POST тот же путь + `/{lockNumber}` | gateway | конкретный замок |
| POST `/vdome-smart-facade/devices/api/v1/cameras/{id}/snapshots` | gateway | пустое тело, Accept: image/webp; бинарное изображение |

Методы взяты из `AuthApi`, `AuthApiGateway`, `IntercomRestApi`,
`RestApiPaths`, `SnapshotUrlBuilder`, `CoilModule`. Наличие метода в APK
не гарантирует доступность на текущем сервере или для конкретного аккаунта.

Приложение также использует SSO refresh, регистрацию push-токенов, вызовные
action-методы, историю событий и медиа-сервисы. Каталог HTTP-аннотаций можно
повторно сформировать:

```sh
python3 scripts/catalog_protocol.py research/decompiled/sources
```

## Существенные ограничения live

1. Пользователь подтвердил успешный SSO JSON-вход и загрузку подъездов после
   переноса Base64 JSON device_id. SMS и автооткрытие на устройстве не проверены.
   Первый перенос камеры ошибочно использовал GET; исправлен по POST-interceptor
   в CoilModule и независимому CameraSnapshotSaver. Ожидается проверка кадра.
2. SIP-параметры в push могут отличаться от fallback WSS/домена аккаунта.
   FCM/MPS клиент не реализован. Постоянная регистрация может быть недостаточна.
   9 ноября после обновления входа сервер подтвердил REGISTER кодом 200 OK.
   Это подтверждает регистрацию, но не доставку вызовов: нужен реальный звонок.
   Исправлена регистрация TCP поверх WSS на WSS, как в Android; продление
   учитывает выданный Expires/Contact expires, не отключая приём во время обновления.
   Короткий INVITE сохраняется до 30 секунд после CANCEL/BYE и разрыва WS,
   чтобы опрос не терял событие. Уже обработанный Call-ID не повторяет unlock.
   На реальном соединении наблюдался EOF/WS 1006 через 120 секунд при ответах
   на WS ping. Продление REGISTER ограничено интервалом 60 секунд, чтобы
   поддерживать активность SIP hop, не дожидаясь выданных 200 секунд срока
   регистрации. Клиент также отвечает на double-CRLF по
   [RFC 7118 §6](https://www.rfc-editor.org/rfc/rfc7118.html#section-6).
3. SIP-звонок сопоставляется только по `From` с `phone` домофона в списке своего
   аккаунта. Несовпадение не даёт команды открытия.
4. X-Call-Session-ID из push не восстанавливается из SIP Call-ID. Адаптер
   использует обычный метод открытия, не выдаёт SIP Call-ID за push session ID.
5. Реальный аккаунт возвращает подписанные RTSP URL в поле `rtsp` списка камер.
   Добавлен серверный RTSP → HLS relay через FFmpeg; на одной реальной камере
   подтверждены HTTP-запуск, плейлист, последовательные MPEG-TS сегменты
   и декодирование кадра. H.264 передаётся без перекодирования. В браузере hls.js/native HLS,
   запуск по кнопке, звук пока отсутствует. RTSP URL и токены браузеру не выдаются.
6. Каталог содержит больше методов, чем реализовано в адаптере. Оплата,
   управление жителями, заявки, чаты, счётчики и телефонный разговор вне каркаса.
7. Список запрашивается с limit=1000 как в APK; пагинация сверх этого пока не
   реализована. Go-сервер рассчитан на небольшое число аккаунтов; сетевые
   операции аккаунта сериализованы менеджером сессий; получение изображений
   выполняется отдельно и не блокирует обработку вызовов.

В ходе разработки ни один реальный unlock не выполнялся. Протокол и режимы
проверяются на локальных фикстурах. Нельзя считать перенос всего приложения
завершённым до устранения ограничений и проверки с собственным устройством.
