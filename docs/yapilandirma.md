# Yapılandırma referansı

🇬🇧 [English](configuration.md) · [Kurulum](kurulum.md) · [Kullanım](kullanim.md)

Sunucu tarafındaki her ayar `NABIZ_` önekli bir ortam değişkenidir. Sunucu
bileşenlerinin yapılandırma dosyası yoktur — Kubernetes'te aynı imaj farklı
ortamlara gittiği için dağıtımın söylediği kazanmak zorundadır.

.NET agent'ı istisna: derlemede üretilen bir `nabiz.json` okur ve ortam
değişkenleri o dosyayı ezer.

---

## nabiz-collector

OTLP alır, ClickHouse'a yazar, topolojiyi çıkarır.

### Depolama

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `NABIZ_CLICKHOUSE_ADDRS` | `localhost:9000` | Virgülle ayrılmış `host:port` listesi |
| `NABIZ_CLICKHOUSE_DATABASE` | `nabiz` | Veritabanı adı; yoksa oluşturulur |
| `NABIZ_CLICKHOUSE_USERNAME` | `default` | |
| `NABIZ_CLICKHOUSE_PASSWORD` | *(boş)* | |
| `NABIZ_CLICKHOUSE_MAX_CONNS` | `8` | En fazla açık bağlantı |
| `NABIZ_RETENTION_DAYS` | `7` | Ham span saklama süresi, ClickHouse TTL'i olarak |

`NABIZ_RETENTION_DAYS` her açılışta tablo TTL'ini günceller; düşürdüğünüzde
etkisi bir sonraki ClickHouse merge'ünde görülür — anlık silme değildir.

### Alım hattı

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `NABIZ_OTLP_GRPC_ADDR` | `:4317` | OTLP/gRPC dinleme adresi |
| `NABIZ_OTLP_HTTP_ADDR` | `:4318` | OTLP/HTTP dinleme adresi |
| `NABIZ_QUEUE_SIZE` | `200000` | Kuyrukta tutulan en fazla span |
| `NABIZ_WORKERS` | `4` | Paralel batch yazıcı |
| `NABIZ_BATCH_SIZE` | `5000` | ClickHouse insert başına span |
| `NABIZ_FLUSH_INTERVAL` | `2s` | Yarım kalmış batch bu süre sonra yazılır |
| `NABIZ_DEBUG_ADDR` | `:8888` | `/stats` ve `/healthz` sunar |
| `NABIZ_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

**Kuyruk bilerek sınırlı.** Dolduğunda span düşürülür ve bir sayaç artar; alıcı
göndereni asla bloke etmez. Yavaş bir ClickHouse, yavaş bir uygulamaya
dönüşmemeli. `/stats` içinde `spans_dropped` artıyorsa bu, `NABIZ_QUEUE_SIZE` ve
`NABIZ_WORKERS` değerlerini yükseltmenin ya da ClickHouse'a kaynak vermenin
işareti — kuyruğu sınırsız yapmanın değil.

### Topoloji

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `NABIZ_TOPOLOGY_PAIR_TTL` | `30s` | `CLIENT` span'in `SERVER` eşini bekleme süresi |
| `NABIZ_TOPOLOGY_FLUSH_INTERVAL` | `15s` | Kenarların yazılma sıklığı |
| `NABIZ_TOPOLOGY_MAX_PENDING_PER_SHARD` | `50000` | Shard başına eşleşmemiş span tavanı |
| `NABIZ_TOPOLOGY_INCLUDE_NODE` | `false` | Kenar anahtarına Kubernetes node'unu ekler |

Ayarlamaya değen tek şey `NABIZ_TOPOLOGY_PAIR_TTL`. Bir `CLIENT` span'i ve onun
`SERVER` span'i farklı süreçlerden geldiği için farklı zamanlarda ulaşır. TTL,
collector'ın eşleşmemiş bir `CLIENT` span'ini eşini beklerken tuttuğu süre. Çok
kısa olursa yavaş bağlantılarda kenarlar kaybolur; çok uzun olursa bekleyen
harita büyür. Harita iki nesilli ve shard'lı — her `TTL/2`'de döner — yani
bellek her hâlükârda sınırlı kalır.

`NABIZ_TOPOLOGY_INCLUDE_NODE`'u açmak kenar kardinalitesini node sayısıyla
çarpar. Node'a özgü bir sorunu kovalarken faydalı, kalıcı ayar olarak pahalı.

---

## nabiz-api

Sorgu ucu ve gömülü arayüz.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `NABIZ_API_ADDR` | `:8080` | Dinleme adresi |
| `NABIZ_CLICKHOUSE_*` | *(yukarıdaki gibi)* | Collector ile aynı depolama ayarları |
| `NABIZ_POSTGRES_DSN` | `postgres://nabiz:nabiz@localhost:5432/nabiz?sslmode=disable` | Denetim düzlemi |
| `NABIZ_ADMIN_EMAIL` | `admin@nabiz.local` | İlk yöneticinin e-postası |
| `NABIZ_ADMIN_PASSWORD` | *(rastgele üretilir)* | Verilmezse üretilir ve loga **bir kez** yazılır |
| `NABIZ_SECRET_KEY` | — | Tanılama jetonlarını şifreler; yoksa jeton saklanmaz |
| `NABIZ_DUMP_DIR` | *(geçici dizin)* | Çekilen dump dosyalarının yeri |
| `NABIZ_DUMP_QUOTA_GB` | `10` | Dump'lar için toplam disk kotası |
| `NABIZ_DUMP_RETENTION_DAYS` | `7` | Bundan eski dump'lar silinir |
| `NABIZ_LOG_LEVEL` | `info` | |

### NABIZ_ADMIN_PASSWORD

Vermezseniz nabiz ilk açılışta rastgele bir parola üretip loga bir kez yazar:

```
level=WARN msg="ilk yönetici oluşturuldu — bu parola bir daha gösterilmeyecek"
  email=admin@nabiz.local parola=<üretilen>
```

Varsayılan parolayla açılan bir yönetim paneli, olmayandan kötüdür; bu yüzden
geri düşülecek bir varsayılan yok. Parola yalnızca ilk kurulumda verilebilir,
sonrasında arayüzden değiştirilir.

### NABIZ_SECRET_KEY

Tanılama jetonları (nabiz'in dump tetiklemesini sağlayan, proje başına sırlar)
bu anahtarla AES-GCM şifreli saklanır. Anahtar yoksa:

- jetonlar hiç saklanmaz,
- arayüz sessizce başarısız olmak yerine nedenini söyler,
- geri kalan her şey çalışmaya devam eder.

`openssl rand -base64 32` ile üretin. Manifest'e değil, Secret'a koyun.
**Anahtar değişirse önceden saklanmış jetonlar açılamaz** ve yeniden girilmeleri
gerekir.

### Dump saklama

Bir temizleyici saatte bir, ayrıca açılışta bir kez koşar. İki kural uygular ve
sonra yetimleri toplar:

1. **Yaş.** `NABIZ_DUMP_RETENTION_DAYS`'ten eski dosyalar silinir.
2. **Kota.** Toplam `NABIZ_DUMP_QUOTA_GB`'yi aşarsa sığana kadar en eskiler
   silinir.
3. **Yetimler.** Veritabanında karşılığı olmayan dosyalar silinir — ama yalnızca
   son 30 dakikada dokunulmamışlarsa, böylece yazılmakta olan bir dump kendi
   altından silinmez.

Kotayı volume'ün gerçek boyutunun **altında** tutun. Kota bir temizlik hedefi,
yazma bariyeri değil: kota dolmadan disk dolarsa yazma temiz bir şekilde
reddedilmek yerine başarısız olur (Kubernetes'te pod tahliye edilir).

---

## nabiz-operator

.NET enstrümantasyonunu enjekte eden admission webhook.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `NABIZ_WEBHOOK_ADDR` | `:9443` | TLS dinleme adresi |
| `NABIZ_STATS_ADDR` | `:8888` | `/stats` ve `/healthz` sunar |
| `NABIZ_NAMESPACE` | `nabiz` | Operator'ün koştuğu namespace |
| `NABIZ_WEBHOOK_SERVICE` | `nabiz-operator` | Sertifikada kullanılan servis adı |
| `NABIZ_WEBHOOK_CONFIG` | `nabiz-dotnet-injector` | Yamalanacak `MutatingWebhookConfiguration` |
| `NABIZ_COLLECTOR_ENDPOINT` | `http://nabiz-collector.<ns>.svc:4317` | Enjekte edilen pod'ların göndereceği adres |
| `NABIZ_INSTRUMENTATION_IMAGE` | `nabiz/dotnet-instrumentation:1.16.0` | Init container imajı |
| `NABIZ_DEFAULT_SAMPLE_RATIO` | `1.0` | Pod belirtmediğinde örnekleme oranı |
| `NABIZ_CLUSTER_NAME` | *(boş)* | Verilirse kaynak niteliği olarak eklenir |

Operator açılışta kendi imzaladığı bir CA üretip webhook yapılandırmasının
`caBundle` alanını kendisi yamalar; cert-manager bağımlılık değildir.

### Pod başına annotation'lar

| Annotation | Varsayılan | Açıklama |
|---|---|---|
| `nabiz.io/inject-dotnet` | — | `"true"` ise enjeksiyon yapılır |
| `nabiz.io/service-name` | pod label'ı | `OTEL_SERVICE_NAME` |
| `nabiz.io/container` | ilk container | Çok container'lı pod'da hedef |
| `nabiz.io/sample-ratio` | `NABIZ_DEFAULT_SAMPLE_RATIO` | Agent tarafı örnekleme oranı |
| `nabiz.io/libc` | `glibc` | Alpine tabanlı imajlarda `musl` |

---

## .NET agent'ı (nabiz.json)

`Nabiz.Agent` eklendikten sonraki ilk derlemede proje klasöründe oluşur ve bir
daha üzerine yazılmaz.

```json
{
  "endpoint": "http://localhost:4317",
  "protocol": "grpc",

  "serviceName": "",
  "serviceNamespace": "",
  "environment": "",

  "sampleRatio": 1.0,
  "enabled": true,

  "additionalSources": ["Npgsql"],
  "headers": {},

  "captureDbStatement": true,
  "captureCodeLocation": true,
  "debug": false
}
```

| Anahtar | Varsayılan | Açıklama |
|---|---|---|
| `endpoint` | `http://localhost:4317` | Collector adresi |
| `protocol` | `grpc` | `grpc` ya da `http/protobuf` |
| `serviceName` | *(assembly adı)* | nabiz'de servis olarak görünür |
| `serviceNamespace` | *(boş)* | Servisleri gruplar, örn. `shop` |
| `environment` | *(boş)* | `production`, `staging`, … |
| `sampleRatio` | `1.0` | `0.1`, on trace'ten birini tutar |
| `enabled` | `true` | `false` agent'ı tamamen kapatır |
| `additionalSources` | `["Npgsql"]` | Dinlenecek ek `ActivitySource` adları |
| `headers` | `{}` | OTLP isteklerine eklenen başlıklar (örn. yetkilendirme) |
| `captureDbStatement` | `true` | Veritabanı span'lerine SQL metnini yazar |
| `captureCodeLocation` | `true` | Kod seviyesi span'lere dosya ve satır yazar |
| `debug` | `false` | Agent etkinliğini konsola yazar |

> `captureDbStatement` **sorguyu** kaydeder, parametre değerlerini değil. Kod
> tabanınız parametre kullanmak yerine değerleri SQL'e gömüyorsa o metin nabiz'e
> ulaşır; o durumda kapatın.

Metot **parametre değerleri hiç kaydedilmez**, yalnızca tipleri. Değerler kişisel
veri, parola ya da jeton taşıyabileceği için agent onları hiç göndermez.

### Ortam değişkeniyle ezme

Her anahtar, `NABIZ_` önekli bir ortam değişkeniyle ezilebilir:

| Değişken | Neyi ezer | Standart karşılığı |
|---|---|---|
| `NABIZ_ENDPOINT` | `endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` |
| `NABIZ_PROTOCOL` | `protocol` | `OTEL_EXPORTER_OTLP_PROTOCOL` |
| `NABIZ_SERVICE_NAME` | `serviceName` | `OTEL_SERVICE_NAME` |
| `NABIZ_SERVICE_NAMESPACE` | `serviceNamespace` | — |
| `NABIZ_ENVIRONMENT` | `environment` | — |
| `NABIZ_SAMPLE_RATIO` | `sampleRatio` | `OTEL_TRACES_SAMPLER_ARG` |
| `NABIZ_ENABLED` | `enabled` | — |
| `NABIZ_DEBUG` | `debug` | — |
| `NABIZ_API_URL` | `apiUrl` | — |
| `NABIZ_CAPTURE_DB_STATEMENT` | `captureDbStatement` | — |
| `NABIZ_CAPTURE_CODE_LOCATION` | `captureCodeLocation` | — |
| `NABIZ_CONFIG` | *(config dosyasının kendi yolu)* | — |

Standart `OTEL_*` değişkenleri geri düşüş olarak dikkate alınır, böylece
OpenTelemetry için zaten yapılandırılmış bir uygulama çalışmaya devam eder.
İkisi birden verilmişse `NABIZ_*` kazanır.

### Tanılama bloğu

Yalnızca `Nabiz.Agent.Diagnostics` referans verildiğinde okunur.

```json
"apiUrl": "http://nabiz-api:8080",
"diagnostics": {
  "enabled": false,
  "path": "/nabiz/diag",
  "token": "",
  "outputDirectory": "",
  "maxFiles": 5,
  "maxCpuSeconds": 120,
  "allowDownload": false,
  "advertisedHost": ""
}
```

| Anahtar | Varsayılan | Açıklama |
|---|---|---|
| `apiUrl` | *(boş)* | Verilirse agent kendini nabiz'e tanıtır |
| `enabled` | `false` | Varsayılan kapalı |
| `path` | `/nabiz/diag` | Tanılama uçlarının yol öneki |
| `token` | *(boş)* | **En az 16 karakter**, yoksa uçlar hiç açılmaz |
| `outputDirectory` | *(geçici dizin)* | Dump'ların uygulama içinde yazılacağı dizin |
| `maxFiles` | `5` | Bunun üstündeki eski dosyalar silinir |
| `maxCpuSeconds` | `120` | Profil süresinin üst sınırı |
| `allowDownload` | `false` | nabiz'in dosyayı HTTP ile çekebilmesi için gerekli |
| `advertisedHost` | *(boş)* | Kaynak IP geri erişilebilir değilse bildirilecek adres |

`advertisedHost` **yalnızca jetonla doğrulanmış kayıtlarda** dikkate alınır.
Doğrulanmamış bir kayıt, nabiz'i jetonu saldırganın seçtiği bir adrese
göndermeye ikna edemez.

Bunların gerçekte neyi açtığı için bkz.
[kullanim.md](kullanim.md#talep-üzerine-dump).

---

## Sorgu parametreleri

Okuma uçları bir zaman aralığı ve isteğe bağlı bir proje kapsamı alır:

| Parametre | Örnek | Açıklama |
|---|---|---|
| `from` | `15m`, `1h`, `24h` | Göreli aralık |
| `from` / `to` | RFC3339 zaman damgası | Mutlak aralık |
| `project` | `sepet` | Her sorguyu tek bir projeye daraltır |
| `level` | `service`, `workload`, `namespace` | Topoloji toplama seviyesi |

```bash
curl "localhost:8080/api/v1/topology?from=1h&level=workload"
curl "localhost:8080/api/v1/traces?service=sepet-servisi&minDurationMs=500&onlyErrors=true"
```
