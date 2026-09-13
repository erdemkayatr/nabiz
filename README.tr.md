# nabiz

**Kubernetes-yerli APM. Topolojiyi ayrı bir keşif mekanizmasından değil, uygulamanın attığı isteklerin kendisinden çıkarır.**

[![CI](https://github.com/erdemkayatr/nabiz/actions/workflows/ci.yml/badge.svg)](https://github.com/erdemkayatr/nabiz/actions/workflows/ci.yml)
[![NuGet](https://img.shields.io/nuget/v/Nabiz.Agent.svg)](https://www.nuget.org/packages/Nabiz.Agent/)
[![Lisans](https://img.shields.io/badge/lisans-Apache--2.0-blue.svg)](LICENSE)

🇬🇧 [English documentation](README.md)

İlk sürümün kapsamı: **.NET** uygulamaları, **Docker** ve **Kubernetes**.

---

## Neden bir APM daha

Çoğu APM'de servis haritası ayrı bir işin ürünüdür: ya elle tanımlarsınız, ya
bir sidecar ağ trafiğini dinler, ya da servis mesh'in telemetrisine bağımlı
kalırsınız. Üçü de kurulum maliyeti ve gerçeklikten sapma demek.

nabiz'de servis haritası, zaten topladığı trace'lerin bir yan ürünüdür. Bir
`SERVER` span'inin ebeveyni, çağıran serviste üretilmiş bir `CLIENT` span'idir;
ikisini `span_id` üzerinden eşleştirince kenarın iki ucu da elde edilir. Ekstra
veri toplanmaz, ekstra bileşen kurulmaz, harita her zaman gerçek trafiği
gösterir.

## Performans duruşu

"Yazılımın performansına etkisi olmasın" gereksinimi üç yerde karşılanır:

| Nerede | Ne yapılıyor |
|---|---|
| Uygulama içi | Enstrümantasyon CLR Profiler API ile çalışma anında eklenir. Kodda, NuGet grafiğinde, derleme çıktısında iz yok. |
| Uygulamadan çıkış | Örnekleme kararı agent'ta verilir — düşürülen span hiç serileştirilmez, ağa çıkmaz. Gönderim batch'li ve arka planda; istek yolunda ağ çağrısı yok. |
| Collector | Alıcı asla göndereni bloke etmez. Kuyruk dolarsa span düşürülür ve sayaç artar; yavaş bir ClickHouse zincirleme olarak uygulamayı yavaşlatamaz. |

Topoloji sıcak yolu, Apple M4 Pro üzerinde ölçüldü:

```
BenchmarkObserve-14    38510395    93.42 ns/op    0 B/op    0 allocs/op
```

Span başına 93 ns ve sıfır allocation — tek çekirdekte saniyede ~10M span'lik
bir tavan. Pratikte darboğaz ClickHouse yazma hızıdır, topoloji hesabı değil.

## Mimari

```
.NET uygulaması                    nabiz-collector              ClickHouse
┌────────────────┐                ┌──────────────────┐        ┌──────────────┐
│ uygulama kodu  │                │ OTLP alıcı       │        │ spans        │
│   (dokunulmaz) │   OTLP/gRPC    │  (gRPC + HTTP)   │        │ service_edges│
│ ┌────────────┐ │ ─────────────► │        ↓         │ ─────► │ operation_.. │
│ │CLR Profiler│ │                │ sınırlı kuyruk   │        │ trace_index  │
│ └────────────┘ │                │        ↓         │        └──────────────┘
└────────────────┘                │ ┌──────┴──────┐  │               ↑
        ▲                         │ │depo │topoloji│  │               │
        │ enjeksiyon              │ └─────┴────────┘  │          nabiz-api
┌───────┴────────┐                └──────────────────┘         (sorgu ucu)
│ nabiz-operator │
│ (admission wh) │
└────────────────┘
```

| Bileşen | Dil | Görev |
|---|---|---|
| `nabiz-collector` | Go | OTLP alır, ClickHouse'a yazar, topolojiyi çıkarır |
| `nabiz-api` | Go | Sorgu ucu + arayüz (tek binary) |
| `nabiz-operator` | Go | Pod'lara .NET enstrümantasyonunu enjekte eden webhook |
| Depolama | ClickHouse | Telemetri; span'lerde ~10x sıkıştırma |
| Denetim düzlemi | PostgreSQL | Kullanıcı, rol grubu, proje, oturum |
| Agent (k8s) | OpenTelemetry .NET auto-instrumentation | Operator enjekte eder, kod değişmez |
| Agent (NuGet) | `Nabiz.Agent` | Pakete referans yeterli; config derlemede oluşur |
| Dump (NuGet) | `Nabiz.Agent.Diagnostics` | Talep üzerine CPU profili ve bellek dump'ı |

Dil seçimi Go: Kubernetes ekosisteminin (client-go, admission webhook'ları) ve
OTLP'nin ana dili. Rust daha yüksek tavan verirdi ama darboğaz bu katmanda
değil, depolamada.

## .NET agent'ı (NuGet)

Kubernetes dışında — geliştirici makinesinde, VM'de, Windows servisinde —
operator yoktur. O durumda agent paketi kullanılır:

```bash
dotnet add package Nabiz.Agent
dotnet build
```

Derlemeden sonra proje klasöründe `nabiz.json` oluşur; `endpoint` alanına
nabiz collector adresini yazarsınız. Dosya bir daha üzerine yazılmaz.

```json
{ "endpoint": "http://nabiz-collector:4317", "serviceName": "sepet-servisi" }
```

Uygulama kodunda tek satır değişiklik gerekmez: paket, derleme sırasında
projeye bir `[ModuleInitializer]` enjekte eder ve agent uygulama açılırken
kendiliğinden devreye girer. Ayrıntılar: [agent/dotnet/Nabiz.Agent/README.md](agent/dotnet/Nabiz.Agent/README.md).

Ortam değişkenleri dosyayı ezer (`NABIZ_ENDPOINT`, `NABIZ_SERVICE_NAME`, …).
Kubernetes'te aynı imaj farklı ortamlara gittiği için imajın içindeki dosyayı
değiştirmek mümkün değildir; dağıtımın söylediği kazanır.

## Talep üzerine dump

Arayüzdeki **Tanılama** menüsünden çalışan örnekleri görür, tek tıkla CPU
profili ya da bellek dump'ı alır ve indirirsiniz. Uygulama tarafında ayrı bir
paket gerekir:

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

```bash
curl -X POST -H "X-Nabiz-Token: $TOKEN" \
  "http://uygulama:5000/nabiz/diag/cpu?seconds=20"

curl -X POST -H "X-Nabiz-Token: $TOKEN" \
  "http://uygulama:5000/nabiz/diag/memory?type=heap"
```

CPU profili ham **nettrace**'tir ve bilerek çözümlenmiyor: PerfView, Visual
Studio ve `dotnet-trace convert` bu biçimi zaten okuyor, kendi
çözümleyicimizi yazmak hatalarını da üstlenmek olurdu. Yerel bir ölçümde
5 saniyelik profil 7.1 MB, 733 kare üretti ve içinde uygulamanın kendi
metotları göründü.

### Nasıl bağlanıyor

```
uygulama ──kayıt (60sn)──> nabiz ──dump isteği──> uygulama ──dosya──> nabiz
```

Agent kendini nabiz'e tanıtır; kayıtta uygulamanın tanılama jetonunu da
gönderir. nabiz bunu projeye tanımlı jetonla karşılaştırır. **Tutmazsa örnek
listelenir ama dump tetiklenemez** — bu olmasaydı sahte bir kayıt, nabiz'i
jetonu saldırganın adresine göndermeye ikna edebilirdi.

Jetonu **Yönetim → Projeler → Düzenle → Tanılama jetonu** alanına girersiniz.
`NABIZ_SECRET_KEY` ile şifrelenerek saklanır ve bir daha gösterilmez; anahtar
yoksa jeton hiç kaydedilmez.

Dump almak ayrı bir yetkidir (`diagnostics.manage`). Trace okumakla aynı şey
değil: bellek dump'ı bağlantı dizesi, jeton ve müşteri verisi içerir.

> **Bellek dump'ı sürecin tüm belleğini diske yazar** — bağlantı dizeleri,
> oturum jetonları, parolalar, müşteri verisi dahil. Uygulama tarafında
> varsayılan **kapalı** ve **en az 16 karakterlik bir jeton** olmadan
> açılmıyor. Bu mimaride nabiz tüm uygulamaların jetonlarını saklar ve
> dosyaları üzerinden geçirir: nabiz'i ele geçiren, izlediği her uygulamanın
> süreç belleğini alabilir.

### İlerleme ve durdurma

Dump'ı başlattığınızda bir ilerleme penceresi açılır. Süre tahmini
verilmiyor: bir bellek dump'ının ne kadar süreceği sürecin belleğine bağlı ve
uydurma bir yüzde çubuğu bekleyeni yanıltmaktan başka işe yaramaz — geçen
süre gösteriliyor, o gerçek bilgidir.

Pencereyi kapatmak işi durdurmaz; arka planda sürer ve biten dosya listede
belirir. Durdurmak ayrı bir düğmedir ve türe göre farklı davranır:

| Tür | Durdur |
|---|---|
| CPU profili | **Kesilir.** O ana kadarki örnekler geçerli bir profil olarak kaydedilir; dosya normal şekilde indirilir. Ölçümde 20 saniyelik bir profil 6. saniyede durduruldu ve 5.4 MB'lık okunabilir bir `.nettrace` çıktı. |
| Bellek dump'ı | **Kesilemez.** `WriteDump` runtime'a gidiyor, runtime süreci askıya alıp dosyayı yazıyor; başladıktan sonra geri dönüşü yok. Pencere bunu söyler ve iş bitene kadar beklenir. |

Durdurma isteği uygulamaya gider. Uygulama işi kesebildiyse nabiz
**beklemeyi sürdürür** ve kısmi dosyayı indirir — beklemeyi bırakmak,
kullanıcının topladığı veriyi çöpe atmak olurdu. nabiz beklemeyi yalnızca
uygulamaya hiç ulaşamadığında bırakır; kayıt o zaman "durduruldu" olur.

Dump alınırken süreç **askıya alınır**: 540 MB'lık bir dump 3.8 saniye sürdü
ve o süre boyunca uygulama istek işlemedi. Üretimde trafiği kesilmiş bir
örnekte alın. Ayrıntılar:
[agent/dotnet/Nabiz.Agent.Diagnostics/README.md](agent/dotnet/Nabiz.Agent.Diagnostics/README.md).

## Kod seviyesi zamanlama

Otomatik enstrümantasyon istekleri, HTTP çağrılarını ve veritabanı sorgularını
görür — aradaki kendi kodunuzu görmez. Bir isteğin 200 ms sürdüğünü bilmek, o
200 ms'in nerede geçtiğini söylemez.

İki yol var. **Otomatik:** DI'a kayıtlı servisleri tek satırla ölçüme alın —
o servislerin bütün metotları kodlarına dokunulmadan kendi span'ini alır.

```csharp
builder.Services.AddScoped<ISepetServisi, SepetServisi>();
builder.Services.AddNabizCodeLevel();   // kayıtlardan SONRA
```

Gürültülü metotları `[NabizIgnore]` ile susturur, span adlarını
`[NabizTrace(Name = "...")]` ile okunur yaparsınız.

**Seçmeli:** ölçmek istediğiniz bloğu işaretleyin; dosya ve satır bilgisi
derleyiciden bedavaya gelir.

```csharp
var fiyat = NabizTracer.Measure("fiyat hesapla", () => Hesapla(sepet));
await NabizTracer.MeasureAsync("stok rezerve et", () => StokAyir(sepet));
```

Trace detayında dört şey görünür:

**Kendi süresi (self time).** Her span için toplam sürenin yanında,
çocuklarında geçmeyen süre ayrı gösterilir. Şelale çubuğu iki katmanlıdır:
soluk kısım toplam, koyu kısım kendi süresi. Bir span'in 200 ms sürmesi onun
yavaş olduğu anlamına gelmez.

```
GET /hesapla        toplam  68.12 ms   kendi   0.33 ms
  sepeti doğrula    toplam  13.02 ms   kendi  13.02 ms   Program.cs:20
  fiyat hesapla     toplam  45.76 ms   kendi   0.02 ms   Program.cs:26
    kampanya uygula toplam  45.74 ms   kendi  45.74 ms   Program.cs:28
  stok rezerve et   toplam   9.02 ms   kendi   9.02 ms   Program.cs:34
```

**Süre nerede geçti.** Kendi kodu / veritabanı / dış servis çağrısı / kuyruk
kırılımı. Kendi süreler toplandığı için dilimlerin toplamı trace süresine
eşittir — bir isteğin yavaş olduğunu görmek yetmez, kendi kodunda mı yoksa
beklediği bir serviste mi yavaş olduğu farklı ekiplere iş düşürür.

```
veritabanı            52.92 ms  %57.9
kendi kodu            37.92 ms  %41.5
dış servis çağrısı     0.62 ms  % 0.7
Servis bazında: sepet-servisi 90.97 ms (%99.5) · sample-backend 0.49 ms (%0.5)
```

**Sıcak noktalar.** Kendi süresine göre sıralanmış özet. Aynı adı taşıyan
span'ler toplanır, böylece N+1 sorgu gibi desenler görünür olur: tek tek 2 ms
süren 80 sorgu listede 160 ms olarak en üste çıkar.

**İstisnalar.** Tür, mesaj ve tam yığın izi span'in üzerinde durur; kod konumu
(dosya:satır) yığın izinin uygulamaya ait ilk karesinden ayıklanır. Ayrı bir
log aramaya gerek kalmaz.

Span'ler ayrıca ayrılan belleği, thread kimliğini ve async thread değişimini
taşır. Parametrelerin yalnızca tipleri kaydedilir; değerler kişisel veri,
parola ya da jeton taşıyabileceği için agent onları hiç göndermez.

Otomatik ölçüm yalnızca **arayüz üzerinden** kayıtlı servisleri kapsar; IL'e
dokunulmaz, çünkü bozuk IL üretmek izlediği uygulamayı çökerten bir araç
demektir. Tek satırı da kaldırmak için `IHostingStartup` denendi ve çalışmıyor:
hosting startup, uygulamanın kendi kayıtlarından önce koşuyor.

## Erişim modeli

Kimlik ve yetkilendirme tek bir zincirde okunur:

```
kullanıcı ──üye──> rol grubu ──bağlı──> proje ──içerir──> uygulama
```

Bir kullanıcı bir uygulamanın telemetrisini, ancak üyesi olduğu bir rol grubu
o uygulamanın projesine bağlıysa görebilir. Yetki doğrudan kullanıcıya
verilmez; "bu kişi bu veriyi neden görüyor?" sorusunun cevabı her zaman bu
zincirde okunabilir.

| Kavram | Nedir |
|---|---|
| **Uygulama** | Telemetri gönderen servis (`service.name`). Elle yazılmaz — collector'ın gördüğü listeden seçilir. |
| **Proje** | Uygulamaların ve erişim yetkisinin toplandığı birim. |
| **Rol grubu** | Yetki kümesi taşıyan kullanıcı grubu. Projeye bağlanan da budur. |
| **Sistem yöneticisi** | Denetim düzleminin tamamını yönetir, tüm veriyi görür. |

Yetkiler: `topology.read`, `services.read`, `traces.read`, `project.manage`,
`diagnostics.manage`, `admin.manage`.

Filtreleme sunucu tarafında, sorgu düzeyinde yapılır. Arayüzde sekme gizlemek
bir güvenlik önlemi değil; adres çubuğundan `#admin/users` yazan bir kullanıcı
da, doğrudan API'yi çağıran bir betik de 403 alır. Erişilemeyen bir kaynak
için 403 yerine boş sonuç dönülür: aksi halde uç, var olan servisleri saymaya
yarayan bir araca dönüşürdü.

### Kimlik notları

- Parolalar bcrypt ile saklanır; oturum jetonunun yalnızca SHA-256 özeti
  veritabanında durur.
- Oturum çerezi `HttpOnly` ve `SameSite=Lax`; TLS arkasında `Secure`.
- Parola değişince kullanıcının tüm oturumları kapanır.
- Sistemdeki son yöneticinin yetkisi alınamaz, pasifleştirilemez, silinemez.
- `NABIZ_ADMIN_PASSWORD` verilmezse ilk açılışta rastgele bir parola üretilir
  ve loga **bir kez** yazılır. Varsayılan parolayla açılan bir yönetim paneli,
  olmayandan kötüdür.

## Hızlı başlangıç (Docker)

```bash
git clone https://github.com/erdemkayatr/nabiz.git
cd nabiz
make up
```

Ayağa kalkanlar: ClickHouse, collector, api, iki .NET örnek servisi, Postgres
ve sürekli trafik üreten bir yük üreteci.

Yaklaşık bir dakika sonra arayüzü açın:

**http://localhost:8080** — `admin@nabiz.local` / `nabiz1234`

> Bu sabit giriş yalnızca compose dosyasındaki yerel geliştirme içindir. Başka
> her kurulumda `NABIZ_ADMIN_PASSWORD` verilmezse rastgele bir parola üretilir
> ve loga **bir kez** yazılır.

Kubernetes ve kaynaktan derleme dahil ayrıntılı yönergeler:
**[docs/kurulum.md](docs/kurulum.md)**.

Beş ekran var:

| Ekran | Ne gösterir |
|---|---|
| **Topoloji** | İsteklerden çıkarılan canlı servis grafiği: dairesel düğümler, kenarlarda çağrı hızıyla akan noktalar. Düğüm sürüklenir, tuval yakınlaştırılır. Düğüme ya da kenara tıklayınca metrik paneli açılır ve ilgisiz kısımlar soluklaşır. Seviye seçici ile servis / k8s workload / k8s namespace görünümleri. |
| **Servisler** | Servis başına hız, hata oranı ve p50/p95/p99. |
| **Trace'ler** | Servis, süre ve hata filtreleriyle arama; satıra tıklayınca şelale görünümü — SQL sorgusu ve HTTP hedefi dahil. |
| **Tanılama** | Çalışan örnekler ve tek tıkla CPU profili / bellek dump'ı. İlerleme penceresi işi izler ve CPU profilini durdurabilir. Alınan dosyalar listelenir ve indirilir. `diagnostics.manage` yetkisi ister. |
| **Yönetim** | Kullanıcılar, rol grupları ve projeler. Proje düzenleme ekranı, telemetri gönderen servisleri listeler; uygulamayı elle yazmak yerine listeden seçersiniz — yazım hatası yüzünden veri göremeyen bir proje oluşmaz. |

Arayüz **Türkçe ve İngilizce**. Dil, tarayıcı diline göre seçilir, üst çubuktan
değiştirilebilir ve tercih saklanır. Sayı ve saat biçimi de dile uyar
(`6.292` / `6,292`, `20:27` / `8:27 PM`).

Arayüz `nabiz-api` binary'sinin içine gömülüdür: ayrı bir statik sunucu,
ConfigMap ya da "API adresi" ayarı yok. Bağımlılık da yok — kuvvet
simülasyonu dahil her şey elde yazıldı, CDN'e erişimi olmayan bir kümede de
açılır. Trace şelalesinin adresi (`#traces/<id>`) paylaşılabilir.

Topoloji 10 saniyede bir tazelenir ama **grafik zıplamaz**: düğüm kümesi
değişmediyse yalnızca sayılar güncellenir, yerleşim olduğu gibi kalır.

Aynı veriye komut satırından bakmak isterseniz:

```bash
make topology
```

İlk çıktı, hiçbir şey yapılandırılmadan şöyle görünür:

```
user                 --  entry   --> shop/sample-frontend   calls=569  hata=3.7%  avg=6.8ms
shop/sample-frontend -- service  --> shop/sample-backend    calls=569  hata=9.0%  avg=6.0ms
shop/sample-backend  -- database --> postgresql:orders      calls=292  hata=0.0%  avg=8.9ms
```

`postgresql:orders` düğümüne dikkat: Postgres'te hiçbir şey kurulu değil.
O kenar, backend'in attığı sorgu span'lerinden türetildi.

Diğer komutlar: `make services` (RED metrikleri), `make stats` (collector iç
sayaçları), `make logs`, `make down`.

## Kubernetes

```bash
kubectl apply -f deploy/k8s/
```

Bir .NET uygulamasını enstrümante etmek için pod şablonuna tek annotation:

```yaml
metadata:
  annotations:
    nabiz.io/inject-dotnet: "true"
```

Operator, admission sırasında pod'a bir init container ve ortam değişkenleri
ekler. Uygulama imajı, Dockerfile'ı ve kodu değişmez; geri almak annotation'ı
silmekten ibarettir.

Kubernetes boyutları (namespace, pod, workload, node) downward API ile pod'un
kendisinden gelir. Collector sıcak yolda Kubernetes API'sine hiç gitmez;
topolojinin k8s boyutu bu yüzden bedavaya gelir:

```bash
# servis seviyesinde (varsayılan)
curl "localhost:8080/api/v1/topology?from=1h"
# deployment seviyesinde
curl "localhost:8080/api/v1/topology?from=1h&level=workload"
# namespace seviyesinde
curl "localhost:8080/api/v1/topology?from=1h&level=namespace"
```

### Annotation referansı

| Annotation | Varsayılan | Açıklama |
|---|---|---|
| `nabiz.io/inject-dotnet` | — | `"true"` ise enjeksiyon yapılır |
| `nabiz.io/service-name` | pod label'ı | `OTEL_SERVICE_NAME` |
| `nabiz.io/container` | ilk container | Çok container'lı pod'da hedef |
| `nabiz.io/sample-ratio` | `1.0` | Agent tarafı örnekleme oranı |
| `nabiz.io/libc` | `glibc` | Alpine tabanlı imajlarda `musl` |

## API

| Uç | Açıklama |
|---|---|
| `GET /` | Arayüz (binary'ye gömülü) |
| `POST /api/v1/auth/login` | Giriş; oturum çerezi bırakır |
| `POST /api/v1/auth/logout` | Çıkış |
| `GET /api/v1/auth/me` | Kim, ne yapabilir, hangi projelere erişir |
| `POST /api/v1/auth/password` | Kendi parolasını değiştirir |
| `GET/POST/PATCH/DELETE /api/v1/admin/users` | Kullanıcı yönetimi |
| `GET/POST/PATCH/DELETE /api/v1/admin/role-groups` | Rol grubu yönetimi |
| `GET/POST/PATCH/DELETE /api/v1/admin/projects` | Proje ve uygulama ataması |
| `GET /api/v1/admin/discovered-applications` | Telemetri gönderen servisler |
| `GET /api/v1/services` | Servis başına hız, hata oranı, p50/p95/p99 |
| `GET /api/v1/operations?service=` | İşlem başına RED metrikleri |
| `GET /api/v1/topology?level=` | Düğümler ve kenarlar |
| `GET /api/v1/traces` | Trace arama (`service`, `minDurationMs`, `onlyErrors`) |
| `GET /api/v1/traces/{traceID}` | Tek trace: span'ler (kendi süresiyle), sıcak noktalar, istisnalar |

Zaman aralığı: `?from=15m` (göreli) veya `?from=<RFC3339>&to=<RFC3339>`.

## Yapılandırma

Tüm ayarlar `NABIZ_` önekli ortam değişkenleridir. Uçları da her sorgu
`?project=<anahtar>` ile tek bir projeye daraltabilir.

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `NABIZ_CLICKHOUSE_ADDRS` | `localhost:9000` | Virgülle ayrılmış |
| `NABIZ_RETENTION_DAYS` | `7` | Ham span saklama süresi |
| `NABIZ_QUEUE_SIZE` | `200000` | Kuyruktaki span üst sınırı |
| `NABIZ_WORKERS` | `4` | Paralel batch işleyici |
| `NABIZ_BATCH_SIZE` | `5000` | ClickHouse batch boyutu |
| `NABIZ_TOPOLOGY_PAIR_TTL` | `30s` | CLIENT span'in eşini bekleme süresi |
| `NABIZ_TOPOLOGY_INCLUDE_NODE` | `false` | Kenar anahtarına k8s node'unu ekler |
| `NABIZ_POSTGRES_DSN` | `postgres://nabiz:nabiz@localhost:5432/nabiz?sslmode=disable` | Denetim düzlemi |
| `NABIZ_ADMIN_EMAIL` | `admin@nabiz.local` | İlk yöneticinin e-postası |
| `NABIZ_ADMIN_PASSWORD` | *(rastgele üretilir)* | Verilmezse loga bir kez yazılır |
| `NABIZ_SECRET_KEY` | — | Tanılama jetonlarını şifreler; yoksa jeton saklanmaz |
| `NABIZ_DUMP_DIR` | *(geçici dizin)* | Çekilen dump dosyalarının yeri |

## Geliştirme

```bash
make build   # derle
make race    # testler, race detector ile
make bench   # sıcak yol ölçümü
make fmt     # gofmt + go vet
```

## Belgeler

| | Türkçe | English |
|---|---|---|
| Kurulum | [docs/kurulum.md](docs/kurulum.md) | [docs/installation.md](docs/installation.md) |
| Kullanım | [docs/kullanim.md](docs/kullanim.md) | [docs/usage.md](docs/usage.md) |
| Yapılandırma referansı | [docs/yapilandirma.md](docs/yapilandirma.md) | [docs/configuration.md](docs/configuration.md) |
| Topoloji iç işleyişi | [docs/topoloji.md](docs/topoloji.md) | [docs/topology.md](docs/topology.md) |
| Katkı | — | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Güvenlik politikası | — | [SECURITY.md](SECURITY.md) |

## Kapsam dışı (şimdilik)

- **Metrikler ve loglar.** Yalnızca trace alınıyor. RED metrikleri span'lerden
  türetiliyor, ayrıca metrik toplanmıyor.
- **Tail-based sampling.** Örnekleme agent tarafında, trace başına baştan
  karar veriliyor. "Önce topla, yavaş/hatalı olanı sakla" henüz yok.
- **Sürekli CPU profilleme.** Dump paketiyle talep üzerine profil alınıyor ama
  bu sürekli değil; arka planda dönen ve trace'lerle ilişkilendirilen bir
  profilci yok.
- **Dump'ların nesne depolamaya taşınması.** Dosyalar yaş ve boyut kotasıyla
  nabiz-api'nin diskinde duruyor; S3 ve benzerine taşımak yazılmadı.
- **Metot içindeki kendi kodu.** Sarmalama servis sınırındadır: bir metodun
  içinde çağırdığınız private yardımcı görünmez. Bunun için derleme anında IL
  weaving gerekiyor. Tasarımı kararlaştırıldı — kapsam `Program.cs`'den
  seçilecek (tüm assembly ya da yalnızca `[NabizTrace]` işaretliler), tüm
  assembly modunda `[NabizIgnore]` ile metot dışlanacak — ama deneysel bayrak
  arkasında ayrı bir dalda geliştirilecek.
- **Sınıf olarak kayıtlı servisler.** Arayüzsüz kayıtlar proxy'lenemez;
  atlananlar açılışta listelenir.
- **CPU süresi ve bekleme süresi ayrımı.** Span'ler duvar saati süresini
  ölçer; "CPU'da mı geçti, kilitte mi bekledi" ayrımı yok.
- **SSO / LDAP.** Kimlik yalnızca e-posta + parola. OIDC bağlamak için
  `internal/identity` altındaki `Authenticate` ve oturum oluşturma noktaları
  yeterli, ama yazılmadı.
- **Denetim günlüğü (audit log).** Yönetim işlemleri sunucu loguna yazılıyor
  ama sorgulanabilir bir tabloda tutulmuyor.
- **Bir trace'in kısmi gizlenmesi.** Trace'in servislerinden biri kapsamdaysa
  tüm span'leri gösterilir. Dağıtık bir izin yarısını saklamak onu
  okunamaz kılardı; yaygın APM davranışı da budur.
- **.NET dışı diller.** Collector zaten OTLP konuşuyor, yani herhangi bir
  OpenTelemetry SDK'sı bugün de veri gönderebilir; otomatik enjeksiyon
  yalnızca .NET için var.

## Lisans

[Apache Lisansı 2.0](LICENSE).
