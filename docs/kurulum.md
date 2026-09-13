# Kurulum

🇬🇧 [English](installation.md) · [Kullanım](kullanim.md) · [Yapılandırma](yapilandirma.md)

nabiz'i çalıştırmanın üç yolu, en hızlıdan üretime en yakına:

1. [Docker Compose](#1-docker-compose) — tüm yığın tek makinede, örnek trafikle
2. [Kubernetes](#2-kubernetes) — manifest'ler ve enjeksiyon yapan admission webhook
3. [Kaynaktan](#3-kaynaktan) — binary'leri kendiniz derleyin

Sonra: [kendi uygulamanızı enstrümante etmek](#kendi-uygulamanızı-enstrümante-etmek).

---

## Gereksinimler

| | Sürüm | Ne için |
|---|---|---|
| Docker | 24+ ve Compose v2 | 1. seçenek |
| Kubernetes | 1.25+ | 2. seçenek |
| Go | 1.26+ | 3. seçenek, geliştirme |
| .NET SDK | 8.0+ | Agent paketlerini ve örnekleri derlemek |
| ClickHouse | 24+ | Telemetri deposu (Compose ve manifest'ler kendi kurulumunu getirir) |
| PostgreSQL | 14+ | Denetim düzlemi (aynı) |

nabiz'in kendisi üç küçük Go binary'si. Ağır olan her şey ClickHouse.

**Boyutlandırma.** Saniyede birkaç bin span üreten bir küme için başlangıç
noktası: ClickHouse 4 vCPU / 8 GB RAM ve saklama sürenize göre disk (span'ler
yaklaşık 10x sıkışıyor; bağlanmadan önce kendi trafiğinizi ölçün). Collector ve
api her biri 500m CPU / 512Mi ile rahat. PostgreSQL yalnızca kullanıcı, rol
grubu, proje ve oturum tutuyor — küçük kalıyor.

---

## 1. Docker Compose

nabiz'i çalışırken görmenin en hızlı yolu. İki örnek .NET servisi ve trafiği
sürekli akıtan bir yük üreteci de geliyor.

```bash
git clone https://github.com/erdemkayatr/nabiz.git
cd nabiz
make up
```

`make up`, `docker compose -f deploy/docker/docker-compose.yml up -d --build`
demek. Ayağa kalkanlar:

| Servis | Yayınlanan port | Nedir |
|---|---|---|
| `clickhouse` | 8123, 9000 | Telemetri deposu |
| `nabiz-postgres` | — | Denetim düzlemi veritabanı (yalnızca iç ağ) |
| `collector` | 4317 (OTLP/gRPC), 4318 (OTLP/HTTP), 8888 (`/stats`, `/healthz`) | OTLP alıcı |
| `api` | 8080 | Sorgu ucu + arayüz |
| `dump-init` | — | Dump volume'ünün sahipliğini bir kez düzeltip çıkar |
| `backend` | 8081 | Örnek .NET servisi (Postgres'e gider) |
| `frontend` | 8082 | Örnek .NET servisi (`backend`'e gider) |
| `postgres` | 5432 | Örnek servislerin kendi veritabanı |
| `loadgen` | — | Sürekli trafik üretir |

`dump-init`'in `Exited` görünmesi normal: dump volume'ü root'a ait olarak
oluşuyor, api ise 10001 kullanıcısıyla koşuyor; tek bir container sahipliği
düzeltip duruyor. Kubernetes'te aynı işi `fsGroup` yapıyor.

Yaklaşık bir dakika verin — ClickHouse'un sağlıklı hale gelmesi ve şemanın
oluşması gerekiyor — sonra açın:

**http://localhost:8080** → `admin@nabiz.local` / `nabiz1234`

### Çalıştığını doğrulamak

```bash
make topology    # isteklerden çıkarılan servis grafiği
make services    # servis başına RED metrikleri
make stats       # collector'ın iç sayaçları
make logs        # collector'ı izle
```

Topoloji boşsa sırayla şunlara bakın:

```bash
# 1. Collector bir şey alıyor mu? spans_received artıyor olmalı.
curl -s localhost:8888/stats | python3 -m json.tool

# 2. ClickHouse sağlıklı mı?
docker compose -f deploy/docker/docker-compose.yml ps

# 3. Örnek servisler gerçekten gönderiyor mu?
docker compose -f deploy/docker/docker-compose.yml logs backend | tail -20
```

`/stats` içinde `spans_dropped` artıyorsa kuyruk dolu demektir: ClickHouse
alımdan yavaş. `NABIZ_QUEUE_SIZE` ve `NABIZ_WORKERS` değerlerini yükseltin ya da
ClickHouse'a kaynak verin. nabiz geri basınç uygulamak yerine span düşürüyor —
bu bilerek böyle: yavaş bir depo, yavaş bir uygulamaya dönüşmemeli.

### Kapatmak

```bash
make down        # container'ları ve volume'leri siler (-v)
```

---

## 2. Kubernetes

```bash
kubectl apply -f deploy/k8s/
```

`nabiz` namespace'ini, ClickHouse'u, PostgreSQL'i, collector'ı, api'yi ve
operator'ü oluşturur. Manifest'ler bilerek düz YAML — Helm chart yok, operator
framework yok — kümenize tam olarak neyin indiğini okuyabilesiniz diye.

Hazır olmasını bekleyin:

```bash
kubectl -n nabiz get pods -w
```

Sonra arayüze ulaşın:

```bash
kubectl -n nabiz port-forward svc/nabiz-api 8080:8080
```

Manifest'ler `NABIZ_ADMIN_PASSWORD`'ü bilerek vermiyor. nabiz ilk açılışta
rastgele bir parola üretip loga **bir kez** yazıyor:

```bash
kubectl -n nabiz logs deploy/nabiz-api | grep "ilk yönetici"
```

```
level=WARN msg="ilk yönetici oluşturuldu — bu parola bir daha gösterilmeyecek"
  email=admin@nabiz.local parola=<üretilen parola>
```

Hemen kaydedin; veritabanından geri alınamaz, yalnızca sıfırlanabilir. Parolayı
kendiniz seçmek isterseniz ilk `apply`'dan önce deployment'a ekleyin — satır içi
değil, Secret'tan:

```bash
kubectl -n nabiz create secret generic nabiz-admin \
  --from-literal=password='uzun-bir-sey-secin'
```

```yaml
- name: NABIZ_ADMIN_PASSWORD
  valueFrom:
    secretKeyRef: { name: nabiz-admin, key: password }
```

### Şifreleme anahtarı

Tanılama jetonları `NABIZ_SECRET_KEY` ile şifreli saklanır. Manifest'ler bunu
**sizin oluşturacağınız** isteğe bağlı bir Secret'tan okur:

```bash
kubectl -n nabiz create secret generic nabiz-secret-key \
  --from-literal=key="$(openssl rand -base64 32)"
```

Anahtar yoksa tanılama jetonları hiç saklanmaz ve arayüz sessizce başarısız
olmak yerine nedenini söyler. Anahtar sonradan değişirse önceden saklanmış
jetonlar açılamaz ve yeniden girilmeleri gerekir.

### Bir .NET pod'unu enstrümante etmek

Pod şablonuna tek annotation:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sepet-servisi
spec:
  template:
    metadata:
      annotations:
        nabiz.io/inject-dotnet: "true"
    spec:
      containers:
        - name: app
          image: registry.example.com/sepet-servisi:1.4.2
```

Operator, admission sırasında pod'a bir init container ve ortam değişkenleri
ekler. Uygulama imajı, Dockerfile'ı ve kodu değişmez; geri almak annotation'ı
silmekten ibarettir.

#### Annotation referansı

| Annotation | Varsayılan | Açıklama |
|---|---|---|
| `nabiz.io/inject-dotnet` | — | `"true"` ise enjeksiyon yapılır |
| `nabiz.io/service-name` | pod label'ı | `OTEL_SERVICE_NAME` |
| `nabiz.io/container` | ilk container | Çok container'lı pod'da hedef |
| `nabiz.io/sample-ratio` | `1.0` | Agent tarafı örnekleme oranı |
| `nabiz.io/libc` | `glibc` | Alpine tabanlı imajlarda `musl` |

#### Webhook sertifikaları

Operator açılışta kendi imzaladığı bir CA üretir ve kendi
`MutatingWebhookConfiguration`'ını bu demetle yamalar. cert-manager gerekmez.
Secret yoksa sertifika yeniden üretilir; bu yüzden Secret'ı silip operator'ü
yeniden başlatmak geçerli bir döndürme yöntemidir.

Operator'de bir sorun olduğunda pod'ların kabulüne ne olacağını webhook'un
`failurePolicy` alanı belirler. `Ignore` olarak geliyor: bozuk bir izleme
sistemi dağıtımlarınızı durduramasın diye. Bedeli, kesinti sırasında oluşan
pod'ların enstrümantasyonsuz açılması ve sonradan yeniden başlatılmaları
gerekmesi.

### Dump volume'ünü boyutlandırmak

api, toplanan dump'ları bir volume'de tutar. Tek bir bellek dump'ı yüzlerce MB
olabilir — büyük bir sürecin `full` türü dump'ı birkaç GB'ı geçebilir.

```yaml
- { name: NABIZ_DUMP_QUOTA_GB,      value: "15" }
- { name: NABIZ_DUMP_RETENTION_DAYS, value: "7" }
```

`NABIZ_DUMP_QUOTA_GB` değerini volume'ün gerçek boyutunun **altında** tutun.
Kotayı en eskiden başlayarak silen bir temizleyici uyguluyor; kota dolmadan disk
dolarsa pod tahliye edilir.

---

## 3. Kaynaktan

```bash
git clone https://github.com/erdemkayatr/nabiz.git
cd nabiz
make build
```

`cmd/collector`, `cmd/api` ve `cmd/operator` bağımsız derlenir:

```bash
go build -trimpath -o bin/nabiz-collector ./cmd/collector
go build -trimpath -o bin/nabiz-api       ./cmd/api
go build -trimpath -o bin/nabiz-operator  ./cmd/operator
```

ClickHouse ve PostgreSQL'i kendiniz sağlarsınız:

```bash
export NABIZ_CLICKHOUSE_ADDRS=localhost:9000
export NABIZ_POSTGRES_DSN='postgres://nabiz:nabiz@localhost:5432/nabiz?sslmode=disable'
export NABIZ_SECRET_KEY='uzun-rastgele-bir-deger'

./bin/nabiz-collector &
./bin/nabiz-api
```

İkisi de açılışta kendi şemasını oluşturur; ayrı bir migration adımı yoktur.

Arayüz `go:embed` ile api binary'sinin içinde, yani `go build` tek derleme
adımı — JavaScript araç zinciri, bundler ya da `node_modules` yok.

### Geliştirme komutları

```bash
make build   # derle
make test    # testler
make race    # testler, race detector ile
make bench   # sıcak yol ölçümü
make fmt     # gofmt + go vet
```

---

## Kendi uygulamanızı enstrümante etmek

### NuGet paketiyle (her yerde)

Geliştirici makinesinde, VM'de, Windows servisinde ya da operator'ün
ulaşamadığı bir container'da:

```bash
dotnet add package Nabiz.Agent
dotnet build
```

Derlemeden sonra proje dosyanızın yanında `nabiz.json` oluşur:

```json
{
  "endpoint": "http://nabiz-collector:4317",
  "serviceName": "sepet-servisi",
  "sampleRatio": 1.0
}
```

`endpoint` alanına collector adresinizi yazın. Dosya bir kez üretilir ve bir
daha üzerine yazılmaz, yani düzenlemeleriniz yeniden derlemede kaybolmaz.
Commit'lemek isteğe bağlı — ortam değişkenleri her hâlükârda ezer:

```bash
NABIZ_ENDPOINT=http://nabiz-collector:4317 NABIZ_SERVICE_NAME=sepet-servisi dotnet run
```

Kod değişikliği gerekmez. Paket, derlemede bir `[ModuleInitializer]` enjekte
eder ve agent uygulamayla birlikte başlar.

> Config dosyası ve module initializer yalnızca çalıştırılabilir projeler için
> üretilir (`OutputType` `Exe` ya da `WinExe`). Pakete referans veren bir sınıf
> kütüphanesi ikisini de almaz — yapılandırmayı okuyan da agent'ı başlatan da
> kütüphane değil, uygulamadır.

### Kod seviyesi zamanlama (isteğe bağlı)

HTTP ve veritabanı span'leri arasındaki kendi metotlarınızı görmek için DI
kayıtlarınızdan sonra tek satır:

```csharp
builder.Services.AddScoped<ISepetServisi, SepetServisi>();
builder.Services.AddNabizCodeLevel();   // kayıtlardan SONRA
```

Sıra önemli: `AddNabizCodeLevel` zaten kayıtlı olanları sarmalar. Bkz.
[kullanim.md](kullanim.md#kod-seviyesi-zamanlama).

### Dump'lar (isteğe bağlı)

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

Sonra `nabiz.json` içinde:

```json
"apiUrl": "http://nabiz-api:8080",
"diagnostics": {
  "enabled": true,
  "token": "en-az-16-karakter-rastgele-bir-deger",
  "allowDownload": true
}
```

Arayüzde de **Yönetim → Projeler → Düzenle → Tanılama jetonu** alanına aynı
jetonu girin. Üretimde açmadan önce
[kullanim.md](kullanim.md#talep-üzerine-dump) bölümünü okuyun — bir bellek
dump'ı sürecin içindeki her şeyi içerir.

---

## Yükseltme

nabiz hem ClickHouse'ta hem PostgreSQL'de kendi şemasını açılışta oluşturur ve
günceller. Yükseltmek, imajları değiştirip yeniden başlatmaktır:

```bash
# Compose
git pull && make up

# Kubernetes
kubectl -n nabiz rollout restart deploy/nabiz-collector deploy/nabiz-api deploy/nabiz-operator
```

Agent paketleri bağımsız sürümlenir. `Nabiz.Agent.Diagnostics`, eşleşen bir
`Nabiz.Agent` sürümüne bağlıdır; ikisini birlikte yükseltin.

---

## Kaldırma

```bash
# Compose — volume'leri de siler
make down

# Kubernetes
kubectl delete -f deploy/k8s/
```

Bir uygulamayı enstrümante etmeyi bırakmak için `nabiz.io/inject-dotnet`
annotation'ını silip pod'u yeniden başlatın ya da `Nabiz.Agent` paket
referansını kaldırıp yeniden derleyin. İkisi de uygulamanızda hiçbir şey
bırakmaz: agent kodunuzu hiç değiştirmedi, yalnızca çalışan sürece bağlandı.
