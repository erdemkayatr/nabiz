# Nabiz.Agent

nabiz APM agent'ı. Paketi eklersiniz, derlersiniz, sunucu adresini yazarsınız.
Uygulama kodunda değişiklik gerekmez.

```bash
dotnet add package Nabiz.Agent
dotnet build
```

Derlemeden sonra proje klasöründe `nabiz.json` oluşur:

```json
{
  "endpoint": "http://localhost:4317",
  "protocol": "grpc",
  "serviceName": "",
  "sampleRatio": 1.0,
  "enabled": true
}
```

`endpoint` alanına nabiz collector adresini yazın. Dosya bir daha üzerine
yazılmaz; sonraki derlemeler değişikliğinizi korur.

## Yapılandırma

| Alan | Varsayılan | Açıklama |
|---|---|---|
| `endpoint` | `http://localhost:4317` | nabiz collector adresi |
| `protocol` | `grpc` | `grpc` (4317) veya `http` (4318) |
| `serviceName` | *(assembly adı)* | Topolojide görünecek ad |
| `serviceNamespace` | — | Ad çakışmasını önler (`shop/api`) |
| `environment` | — | `prod`, `staging`, `dev` |
| `sampleRatio` | `1.0` | 0.0–1.0 arası örnekleme |
| `enabled` | `true` | `false` ise agent hiç başlamaz |
| `additionalSources` | *(yaygın kütüphaneler)* | Ek ActivitySource adları |
| `codeLevel.enabled` | `true` | Otomatik metot ölçümü |
| `codeLevel.includeNamespaces` | *(giriş assembly kökü)* | Ölçülecek namespace'ler |
| `codeLevel.excludeNamespaces` | `[]` | Dışlanacak namespace'ler |
| `codeLevel.captureAllocations` | `true` | Span başına ayrılan bellek |
| `codeLevel.captureThread` | `true` | Thread kimliği ve async geçişi |
| `codeLevel.captureParameterTypes` | `true` | Parametre tipleri (değerler asla) |
| `headers` | `{}` | OTLP başlıkları (ingress kimlik doğrulaması) |
| `captureDbStatement` | `true` | `false` ise SQL metni span'den silinir |
| `captureCodeLocation` | `true` | Hatalı span'lere dosya:satır eklenir |
| `debug` | `false` | Agent'ın kendi tanılama çıktısı |

Her alan ortam değişkeniyle ezilebilir: `NABIZ_ENDPOINT`,
`NABIZ_SERVICE_NAME`, `NABIZ_SAMPLE_RATIO`, `NABIZ_ENABLED` …

Öncelik sırası: **ortam değişkeni > nabiz.json > varsayılan**. Kubernetes'te
aynı imaj farklı ortamlara gittiği için imajın içindeki dosyayı değiştirmek
mümkün değildir; dağıtımın söylediği kazanır.

Standart `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_SERVICE_NAME` ve
`OTEL_RESOURCE_ATTRIBUTES` de okunur — nabiz operator'ının enjekte ettiği
pod'larda ek ayar gerekmez.

## MSBuild ayarları

```xml
<PropertyGroup>
  <NabizConfigFile>nabiz.json</NabizConfigFile>
  <NabizGenerateConfig>true</NabizGenerateConfig>
  <NabizAutoStart>true</NabizAutoStart>
</PropertyGroup>
```

`NabizAutoStart` kapalıysa agent'ı kendiniz başlatırsınız:

```csharp
Nabiz.Agent.NabizAgent.Start();
```

## Otomatik metot seviyesi ölçüm

Otomatik enstrümantasyon istekleri, HTTP çağrılarını ve veritabanı sorgularını
görür — aradaki kendi kodunuzu görmez. Servislerinizi tek satırla ölçüme
alabilirsiniz:

```csharp
builder.Services.AddScoped<ISepetServisi, SepetServisi>();
builder.Services.AddScoped<IFiyatServisi, FiyatServisi>();

builder.Services.AddNabizCodeLevel();   // kayıtlardan SONRA
```

Bu noktadan sonra bu servislerin **bütün metotları**, kodlarına dokunulmadan
kendi span'ini alır: metot adı, süresi, kendi süresi, ayrılan bellek, thread
kimliği ve async thread değişimi.

Parametrelerin yalnızca **tipleri** kaydedilir. Değerler asla gönderilmez:
parametre kişisel veri, parola ya da jeton taşıyabilir.

### Neden tek satır gerekiyor

Dynatrace gibi araçlar CLR Profiler API'si ile çalışma anında IL'i yeniden
yazar ve hiçbir işaret gerekmeden her metodu görür. Bu paket IL'e dokunmaz:
bozuk IL üretmek, izlediği uygulamayı çökerten bir gözlemlenebilirlik aracı
demektir.

`IHostingStartup` ile bu satırı da kaldırmak denendi ve çalışmıyor: hosting
startup, uygulamanın kendi servis kayıtlarından **önce** çalışıyor ve
sarmalanacak servisleri henüz göremiyor.

### Gürültüyü susturma

Tüm servisleri ölçüme aldığınızda bazı metotlar gürültüden başka bir şey
üretmez: sıkı döngüde çağrılan minik kontroller, her istekte yüzlerce kez
koşan erişimciler. Bunları işaretleyin:

```csharp
[NabizIgnore]
public bool GecerliAdet(int adet) => adet > 0 && adet < 1000;

[NabizTrace(Name = "birim fiyat oku")]
public decimal BirimFiyat(string urunKodu) { ... }
```

`[NabizIgnore]` metoda ya da tipe konabilir. `[NabizTrace]` span'e okunur bir
ad verir. İkisi birlikte bulunursa dışlama kazanır: susturma kararı her zaman
ölçme kararını yener.

### Sınırlar

- Yalnızca **arayüz üzerinden** kayıtlı servisler sarmalanır. Sınıf olarak
  kayıtlı servisler için metotların `virtual` olması gerekirdi; o da sessizce
  eksik ölçüm üretir. Kapsamdaki sınıf kayıtları atlanır ve açılışta listelenir
  — "neden bu servisi göremiyorum" sorusu loglarda cevaplanmalı.
- **Metodun içindeki kendi kodunu göremez.** Sarmalama servis sınırındadır;
  bir metodun içinde çağırdığınız private yardımcı görünmez. Bunun için
  derleme anında IL weaving gerekiyor (aşağıya bakın).
- `ValueTask` döndüren metotlarda yalnızca senkron kısım ölçülür. `AsTask()`
  çağırmak çağıranın elinden sonucu alırdı; span bu durumu açıkça işaretler.
- Varsayılan olarak yalnızca giriş assembly'sinin kök namespace'i taranır.
  Farklı bir kapsam için: `AddNabizCodeLevel(o => o.IncludeNamespaces.Add("Shop."))`

## Yol haritası: IL weaving

Metot içindeki private çağrıları da görmek derleme anında IL'e dokunmayı
gerektiriyor. Tasarım kararlaştırıldı, henüz yazılmadı:

- Kapsam `Program.cs`'den seçilir: **tüm uygulama assembly'si** ya da
  **yalnızca `[NabizTrace]` işaretliler**.
- Tüm assembly modunda istemediğiniz metotları `[NabizIgnore]` ile dışlarsınız
  — bu attribute bugün de çalışıyor, weaving geldiğinde aynı anlamı taşıyacak.
- Deneysel bayrak arkasında ayrı bir dalda geliştirilecek; olgunlaşana kadar
  varsayılan değişmeyecek.

## Neler toplanır

ASP.NET Core istekleri, `HttpClient` çağrıları, SQL Server sorguları ve
kendi `ActivitySource`'unu yayınlayan yaygın kütüphaneler: PostgreSQL
(Npgsql), MySQL, Kafka, RabbitMQ, MassTransit, Elasticsearch, MongoDB,
Quartz, YARP, Azure SDK. Bu kaynakları dinlemek bedavadır — kütüphane yoksa
kaynak hiç yayın yapmaz. Trace bağlamı `traceparent`
başlığıyla servisler arasında taşınır; nabiz servis topolojisini bu
ilişkilerden çıkarır.

## Uygulamaya etkisi

- Örnekleme kararı burada verilir: düşen span serileştirilmez, ağa çıkmaz.
- Gönderim arka planda ve toplu; istek yolunda ağ çağrısı yoktur.
- Kuyruk dolarsa span düşer. Uygulamanın yavaşlaması yerine telemetri kaybı
  tercih edilir.
- Agent başlatılamazsa istisna fırlatmaz, uygulama normal çalışmaya devam
  eder.
