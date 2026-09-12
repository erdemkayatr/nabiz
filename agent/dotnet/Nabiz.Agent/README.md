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
| `additionalSources` | `["Npgsql"]` | Ek ActivitySource adları |
| `headers` | `{}` | OTLP başlıkları (ingress kimlik doğrulaması) |
| `captureDbStatement` | `true` | `false` ise SQL metni span'den silinir |
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

## Neler toplanır

ASP.NET Core istekleri, `HttpClient` çağrıları ve `additionalSources`
listesindeki kaynaklar (varsayılan olarak Npgsql). Trace bağlamı `traceparent`
başlığıyla servisler arasında taşınır; nabiz servis topolojisini bu
ilişkilerden çıkarır.

## Uygulamaya etkisi

- Örnekleme kararı burada verilir: düşen span serileştirilmez, ağa çıkmaz.
- Gönderim arka planda ve toplu; istek yolunda ağ çağrısı yoktur.
- Kuyruk dolarsa span düşer. Uygulamanın yavaşlaması yerine telemetri kaybı
  tercih edilir.
- Agent başlatılamazsa istisna fırlatmaz, uygulama normal çalışmaya devam
  eder.
