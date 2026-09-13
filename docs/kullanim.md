# Kullanım

🇬🇧 [English](usage.md) · [Kurulum](kurulum.md) · [Yapılandırma](yapilandirma.md)

- [Beş ekran](#beş-ekran)
- [Topoloji](#topoloji)
- [Servisler](#servisler)
- [Trace'ler](#traceler)
- [Kod seviyesi zamanlama](#kod-seviyesi-zamanlama)
- [Talep üzerine dump](#talep-üzerine-dump)
- [Yönetim ve erişim denetimi](#yönetim-ve-erişim-denetimi)
- [HTTP API](#http-api)
- [Sorun giderme](#sorun-giderme)

---

## Beş ekran

| Ekran | Yetki | Ne işe yarar |
|---|---|---|
| Topoloji | `topology.read` | Hangi servis hangisiyle konuşuyor, ne kadar sağlıklı |
| Servisler | `services.read` | Servis başına hız, hata, gecikme |
| Trace'ler | `traces.read` | Tek bir isteğin baştan sona hikâyesi |
| Tanılama | `diagnostics.manage` | CPU profili ve bellek dump'ı |
| Yönetim | `admin.manage` | Kullanıcılar, rol grupları, projeler |

Üst çubuktaki dil seçicisi Türkçe ve İngilizce arasında geçer. İlk seçim
tarayıcı diline göre yapılır, sizin seçiminiz saklanır. Sayı ve saat biçimi de
dile uyar.

Zaman aralığı seçicisi (15 dakika / 1 saat / 6 saat / 24 saat) her ekrana
uygulanır. Topoloji 10 saniyede bir tazelenir.

---

## Topoloji

Grafik trace'lerin kendisinden çıkarılır. Hiçbir şey yapılandırılmaz, ayrı bir
mekanizmayla keşfedilmez — bir servis yeni bir bağımlılığı çağırmaya
başladığında kenar bir sonraki tazelemede belirir.

**Okuma:**

- **Düğüm boyutu** trafik hacmi.
- **Düğüm rengi** tür: servis, veritabanı, kuyruk, dış bağımlılık.
- **Kırmızı**, %1'in üzerinde hata oranı demek.
- **Kenar üzerinde akan noktalar** çağrı hızıyla hareket eder — yoğun bir kenar
  gözle görülür şekilde akar, boşta olan akmaz.

**Etkileşim:**

- Düğümü **sürükleyin**, istediğiniz yere sabitleyin.
- **Tekerlek ya da yakınlaştırma çubuğu** ile ölçekleyin; "sığdır" hepsini
  çerçeveler.
- **Düğüme ya da kenara tıklayın**, metrik paneli açılır. Grafiğin ilgisiz
  kısımları soluklaşır, tek bir yolu takip edebilirsiniz.
- **Seviye seçicisi** servis / Kubernetes workload / Kubernetes namespace
  görünümleri arasında geçer.

Yerleşim tazelemede zıplamaz. Düğüm kümesi değişmediyse yalnızca sayılar
güncellenir, konumlar bıraktığınız yerde kalır — on saniyede bir yeniden
dizilen bir grafikle olay takip edilemez.

Aynı veri komut satırından:

```bash
make topology
curl "localhost:8080/api/v1/topology?from=1h&level=workload"
```

### Veritabanı düğümleri nereden geliyor

Veritabanınıza hiçbir şey kurulmuyor. `postgresql:orders` gibi bir düğüm,
uygulamanızın ürettiği sorgu span'lerinden türetiliyor — agent her sorgu
span'ine veritabanı sistemini ve adını yazıyor, topoloji bunları düğüm ve kenara
çeviriyor. Agent çalıştırmayan servislere atılan HTTP çağrıları için de aynısı
geçerli: dış bağımlılık olarak görünürler.

---

## Servisler

Giriş span'lerinden hesaplanan, servis başına hız, hata oranı ve p50/p95/p99.
İşlem bazında kırılım için servise tıklayın.

Yüzdelikler seçili aralıktaki span'lerden geliyor, yani geniş bir aralık
sivrilikleri yumuşatır. Bir dağıtımı "öncesi" ile karşılaştırırken aralığı
ikisini birden kapsayacak şekilde genişletmek yerine, dağıtımın iki yanına
daraltın.

---

## Trace'ler

Servis, en az süre ve yalnızca hatalılar filtreleriyle arayın, satıra tıklayın,
şelale açılsın.

Şelale her span için şunları gösterir:

- **Toplam süre** ve **kendi süresi** yan yana. Çubuk iki katmanlı: soluk kısım
  toplam, koyu kısım kendi süresi.
- Span kod seviyesi zamanlamadan geldiyse **kod konumu** (`Program.cs:26`).
- Veritabanı span'lerinde **SQL sorgusu**, HTTP span'lerinde **hedef adres**.
- **İstisnalar**: tür, mesaj ve tam yığın izi.

Bir span'in 200 ms sürmesi onun yavaş olduğu anlamına gelmez. Kendi süresi,
çocuklarında geçmeyen süredir:

```
GET /hesapla        toplam  68.12 ms   kendi   0.33 ms
  sepeti doğrula    toplam  13.02 ms   kendi  13.02 ms   Program.cs:20
  fiyat hesapla     toplam  45.76 ms   kendi   0.02 ms   Program.cs:26
    kampanya uygula toplam  45.74 ms   kendi  45.74 ms   Program.cs:28
  stok rezerve et   toplam   9.02 ms   kendi   9.02 ms   Program.cs:34
```

45.76 ms ile sorun `fiyat hesapla` gibi görünüyor; aslında altındaki
`kampanya uygula`.

### Süre nerede geçti

Şelalenin altında, sürenin nereye gittiği — kategori ve servis bazında:

```
veritabanı            52.92 ms  %57.9
kendi kodu            37.92 ms  %41.5
dış servis çağrısı     0.62 ms  % 0.7

Servis bazında: sepet-servisi 90.97 ms (%99.5) · sample-backend 0.49 ms (%0.5)
```

Kendi süreler toplandığı için dilimlerin toplamı trace süresine eşittir. Bir
isteğin yavaş olduğunu görmek yetmez: kendi kodunda mı yoksa beklediği bir
serviste mi yavaş olduğu farklı ekiplere iş düşürür.

### Sıcak noktalar

Kendi süresine göre sıralanmış özet; aynı adı taşıyan span'ler toplanır. N+1
sorgu desenleri böyle görünür olur: tek tek 2 ms süren seksen sorgu ayrı ayrı
görünmez, listede tek bir 160 ms satırı olarak en üste çıkar.

### Paylaşmak

Bir trace'in adresi (`#traces/<id>`) paylaşılabilir — bileti açan kişi aynı
şelaleye düşer.

---

## Kod seviyesi zamanlama

Otomatik enstrümantasyon istekleri, HTTP çağrılarını ve veritabanı sorgularını
görür. Aradaki kendi kodunuzu görmez — süre genelde orada geçer.

### Otomatik: DI'a kayıtlı servislerin bütün metotları

```csharp
builder.Services.AddScoped<ISepetServisi, SepetServisi>();
builder.Services.AddScoped<IStokServisi, StokServisi>();

builder.Services.AddNabizCodeLevel();   // kayıtlardan SONRA
```

O servislerin her metodu artık kendi span'ini alır, kodlarına dokunulmadan.
`AddNabizCodeLevel` **zaten kayıtlı** olanları sarmalar, bu yüzden kayıtlardan
sonra gelmek zorunda — sonradan kaydedilenler sarmalanmaz.

**Yalnızca arayüz üzerinden kayıtlı servisleri kapsar.** Sınıf olarak yapılan
kayıtlar proxy'lenemez; atlananlar açılışta listelenir, bir servisin neden
görünmediğini tahmin etmek zorunda kalmazsınız.

> Bunu sıfır satırla yapmak denendi ve olmuyor: `IHostingStartup`, uygulamanın
> kendi kayıtlarından *önce* koşuyor, o noktada sarmalanacak bir şey yok. Tek
> satır, dürüst asgari.

Gürültüyü attribute'larla ayarlarsınız:

```csharp
public class SepetServisi : ISepetServisi
{
    [NabizTrace(Name = "sepeti doğrula")]   // okunur span adı
    public Task<bool> DogrulaAsync(Sepet sepet) { ... }

    [NabizIgnore]                           // döngüde çağrılıyor, ilginç değil
    public decimal SatirToplami(SepetSatiri satir) { ... }
}
```

### Seçmeli: bloğu işaretleyin

Bütün bir servis yerine tek bir bloğu ölçmek istediğinizde. Dosya ve satır
bilgisi derleyiciden bedavaya gelir:

```csharp
var fiyat = NabizTracer.Measure("fiyat hesapla", () => Hesapla(sepet));
await NabizTracer.MeasureAsync("stok rezerve et", () => StokAyirAsync(sepet));

using var span = NabizTracer.Start("kampanya uygula");
KampanyalariUygula(sepet);

using var oto = NabizTracer.Start();   // çağıran metodun adıyla
```

`Measure` / `MeasureAsync` bir ifadeyi sarar; `Start`, blok lambda'ya sığmadığında
kullanılan dispose edilebilir bir kapsam döner. İkisi de dosya ve satırı
derleyiciden alır.

### Bir span ne taşır

Süre, kendi süresi, kod konumu (dosya ve satır), ayrılan bellek, thread kimliği
ve async devamın farklı bir thread'e geçip geçmediği.

**Parametre değerleri hiç kaydedilmez** — yalnızca tipleri. Değerler kişisel
veri, parola ya da jeton taşıyabileceği için agent onları hiç göndermez.

### Neyi kapsamaz

Sarmalama **servis sınırındadır**. Bir metodun içinde çağırdığınız private
yardımcı görünmez:

```csharp
public async Task<decimal> HesaplaAsync(Sepet sepet)   // ← span
{
    var taban = SatirlariTopla(sepet);                 // ← span yok (private)
    var indirim = await _kampanya.UygulaAsync(sepet);  // ← span (DI servisi)
    return taban - indirim;
}
```

Metodun içini görmek derleme anında IL weaving gerektiriyor. Tasarımı hazır ama
bilerek burada değil: bozuk IL üretmek, izlediği uygulamayı çökerten bir izleme
aracı demek. Deneysel bayrak arkasında ayrı bir dalda geliştiriliyor.

---

## Talep üzerine dump

> **Önce bunu okuyun.** Bir bellek dump'ı **sürecin tüm belleğini** diske yazar:
> bağlantı dizeleri, oturum jetonları, parolalar, müşteri verisi. O dosyayı
> indirebilen herkes bunların hepsine erişir.
>
> Bu mimaride nabiz tüm uygulamaların tanılama jetonlarını saklar ve dosyaları
> üzerinden geçirir. **nabiz'i ele geçiren, izlediği her uygulamanın süreç
> belleğini alabilir.** Bu takas, arayüzden tek tıkla dump almayı satın alıyor;
> ortamınızda kabul edilebilir değilse `diagnostics.enabled` kapalı kalsın ve
> dump'ları `dotnet-dump` ile `kubectl cp` kullanarak alın.

### Kurmak

Uygulama tarafı:

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

```json
"apiUrl": "http://nabiz-api:8080",
"diagnostics": {
  "enabled": true,
  "token": "en-az-16-karakter-rastgele-bir-deger",
  "allowDownload": true
}
```

nabiz tarafı: **Yönetim → Projeler → Düzenle → Tanılama jetonu** alanına aynı
jetonu girin. `NABIZ_SECRET_KEY` ile şifrelenerek saklanır ve bir daha
gösterilmez; o anahtar yoksa hiç saklanmaz.

### Nasıl bağlanıyor

```
uygulama ──kayıt (60sn)──> nabiz ──dump isteği──> uygulama ──dosya──> nabiz
```

Agent kendini tanıtırken jetonu da gönderir. nabiz bunu projeye tanımlı jetonla
karşılaştırır. **Tutmazsa örnek listelenir ama dump tetiklenemez** — aksi halde
sahte bir kayıt, nabiz'i jetonu saldırganın adresine göndermeye ikna edebilirdi.
Aynı sebeple istek, agent'ın iddia ettiği adrese değil kaydın geldiği IP'ye
atılır; `advertisedHost` yalnızca doğrulanmış kayıtlarda dikkate alınır (NAT,
vekil, Docker Desktop gibi kaynak IP'nin geri erişilebilir olmadığı durumlar
için).

### Dump almak

Çalışan bir örnekte **Tanılama → CPU profili al** ya da **bellek dump'ı al**.
Bir ilerleme penceresi açılır.

Süre tahmini verilmez. Bir bellek dump'ının ne kadar süreceği sürecin belleğine
bağlı ve uydurma bir yüzde çubuğu bekleyeni yanıltmaktan başka işe yaramaz;
onun yerine geçen süre gösterilir, çünkü o gerçek bilgidir.

**Pencereyi kapatmak işi durdurmaz.** Arka planda sürer ve biten dosya listede
belirir. Durdurmak ayrı bir düğmedir ve türe göre farklı davranır:

| Tür | Durdur |
|---|---|
| CPU profili | **Kesilir.** O ana kadarki örnekler geçerli bir profil olarak saklanır ve normal şekilde indirilir. Ölçüm: 20 saniyelik bir profil 6. saniyede durduruldu, 5.4 MB okunabilir `.nettrace` çıktı. |
| Bellek dump'ı | **Kesilemez.** `WriteDump` runtime'a gidiyor, runtime süreci askıya alıp dosyayı yazıyor; başladıktan sonra geri dönüşü yok. Pencere bunu söyler ve iş bitene kadar sürer. |

Uygulama işi kesebildiyse nabiz **beklemeyi sürdürür** ve kısmi dosyayı indirir
— bırakmak, topladığınız veriyi çöpe atmak olurdu. nabiz beklemeyi yalnızca
uygulamaya hiç ulaşamadığında bırakır; kayıt o zaman *durduruldu* olur,
*başarısız* değil.

### Maliyet

Bellek dump'ı **süreci askıya alır**. Yerel ölçüm: 540 MB'lık bir dump 3.8
saniye sürdü ve o süre boyunca uygulama istek işlemedi. Üretimde trafiği
kesilmiş bir örnekte alın.

Aynı anda tek işlem koşar. İki bellek dump'ı birlikte alınırsa süreç iki kez
askıya alınır ve zaten sıkıntıda olan bir uygulama büsbütün durur.

CPU profili, örnekleme süresince ölçülebilir ek yük demektir. Kısa pencereler
kullanın; `maxCpuSeconds` üst sınırı korur.

### Çıktıyı okumak

CPU profili ham **nettrace**'tir, bilerek çözümlenmez. PerfView, Visual Studio
ve `dotnet-trace convert` bu biçimi zaten okuyor; kendi çözümleyicimizi yazmak
hatalarını da üstlenmek olurdu.

```bash
dotnet-trace convert cpu-20260913-123931.nettrace --format speedscope
```

Bellek dump'ı için `dotnet-dump analyze` ya da Visual Studio.

Dump türleri: `heap` (varsayılan), `full`, `mini`, `triage`. `full` tüm adres
alanını yazar ve gigabaytlarca olabilir — testte `heap` dump'ı 540 MB olan bir
süreç, `full` ile 6.3 GB üretti.

### Saklama

**Tanılama** ekranında bir kullanım çubuğu var: dosya sayısı, kotaya karşı
kullanılan bayt ve saklama süresi. Bir temizleyici yaşa göre ve kota aşıldığında
en eskiden başlayarak siler. Bkz.
[yapilandirma.md](yapilandirma.md#dump-saklama).

---

## Yönetim ve erişim denetimi

Zincir:

```
kullanıcı ──üye──> rol grubu ──bağlı──> proje ──içerir──> uygulama
```

Bir kullanıcı bir uygulamanın telemetrisini, ancak üyesi olduğu bir rol grubu o
uygulamanın projesine bağlıysa görür. Yetki doğrudan kullanıcıya verilmez; "bu
kişi bu veriyi neden görüyor?" sorusunun cevabı her zaman bu zincirde okunur.

| Kavram | Nedir |
|---|---|
| **Uygulama** | Telemetri gönderen servis (`service.name`). Elle yazılmaz — collector'ın gördüğü listeden seçilir. |
| **Proje** | Uygulamaların ve erişim yetkisinin toplandığı birim. |
| **Rol grubu** | Yetki kümesi taşıyan kullanıcı grubu. Projeye bağlanan budur. |
| **Sistem yöneticisi** | Denetim düzleminin tamamını yönetir, tüm veriyi görür. |

Yetkiler: `topology.read`, `services.read`, `traces.read`, `project.manage`,
`diagnostics.manage`, `admin.manage`.

### Bir ekip kurmak

1. **Yönetim → Projeler → Yeni.** Ad ve anahtar verin.
2. Proje düzenleme ekranında uygulamalarını keşfedilmiş listeden seçin. Gerçekten
   telemetri gönderenler arasından seçtiğiniz için yazım hatası yüzünden sessizce
   hiçbir şey göstermeyen bir proje oluşmaz.
3. **Yönetim → Rol grupları → Yeni.** Yetkileri işaretleyin.
4. Rol grubunu projeye bağlayın.
5. **Yönetim → Kullanıcılar → Yeni.** Kullanıcıları rol grubuna ekleyin.

### Sunucu tarafında uygulanır

Filtreleme arayüzde değil, sorgu düzeyinde yapılır. Sekme gizlemek bir güvenlik
önlemi değildir: adres çubuğuna `#admin/users` yazan bir kullanıcı da, doğrudan
API'yi çağıran bir betik de 403 alır.

Erişilemeyen bir kaynak için 403 yerine boş sonuç dönülür: aksi halde uç, var
olan servisleri saymaya yarayan bir araca dönüşürdü.

### Kimlik notları

- Parolalar bcrypt ile saklanır; veritabanında oturum jetonunun yalnızca SHA-256
  özeti durur.
- Oturum çerezi `HttpOnly` ve `SameSite=Lax`; TLS arkasında `Secure`.
- Parola değişince kullanıcının tüm oturumları kapanır.
- Sistemdeki son yöneticinin yetkisi alınamaz, pasifleştirilemez, silinemez.

---

## HTTP API

Arayüzün yaptığı her şey sizin de yapabileceğiniz bir HTTP çağrısı. Kimlik
doğrulama, `/api/v1/auth/login`'den gelen oturum çerezi.

```bash
curl -c jar -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@nabiz.local","password":"nabiz1234"}'

curl -b jar "localhost:8080/api/v1/services?from=1h"
```

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
| `PUT /api/v1/admin/projects/{id}/diagnostics-token` | Projenin tanılama jetonunu ayarlar |
| `GET /api/v1/services` | Servis başına hız, hata oranı, p50/p95/p99 |
| `GET /api/v1/operations?service=` | İşlem başına RED metrikleri |
| `GET /api/v1/topology?level=` | Düğümler ve kenarlar |
| `GET /api/v1/traces` | Trace arama (`service`, `minDurationMs`, `onlyErrors`) |
| `GET /api/v1/traces/{traceID}` | Tek trace: kendi süresiyle span'ler, sıcak noktalar, istisnalar |
| `GET /api/v1/diagnostics/instances` | Kayıtlı agent örnekleri |
| `POST /api/v1/diagnostics/instances/{id}/capture` | Dump başlatır; `202` ve bir kayıt kimliği döner |
| `GET /api/v1/diagnostics/artifacts` | Alınan dosyalar ve disk kullanımı |
| `GET /api/v1/diagnostics/artifacts/{id}` | Tek kaydın durumu |
| `POST /api/v1/diagnostics/artifacts/{id}/cancel` | Koşan işi durdurur |
| `GET /api/v1/diagnostics/artifacts/{id}/download` | Dosyayı indirir |
| `DELETE /api/v1/diagnostics/artifacts/{id}` | Siler |

Zaman aralığı: `?from=15m` (göreli) ya da `?from=<RFC3339>&to=<RFC3339>`. Her
sorgu `?project=<anahtar>` ile tek bir projeye daraltılabilir.

Dump tetikleme beklemek yerine `202 Accepted` döner: bellek dump'ı dakikalar
sürebilir ve tarayıcı beklerken zaman aşımına uğrardı. Durum için
`/api/v1/diagnostics/artifacts/{id}` yoklanır.

---

## Sorun giderme

**Topoloji boş.**
Collector alıyor mu bakın: `curl -s localhost:8888/stats`. `spans_received`
artmıyorsa sorun uygulamanızla collector arasında — `nabiz.json` içindeki
`endpoint`'i ya da Kubernetes'te enjeksiyon annotation'ını kontrol edin.

**Span'ler düşüyor.**
`spans_dropped` artıyorsa kuyruk dolu ve ClickHouse alımdan yavaş demektir.
`NABIZ_QUEUE_SIZE` ve `NABIZ_WORKERS` değerlerini yükseltin ya da ClickHouse'a
kaynak verin. Bu tasarım gereği: span düşürmek, uygulamayı yavaşlatmaktan iyidir.

**İki servis arasında kenar eksik.**
Bir `CLIENT` span'i eşi için `NABIZ_TOPOLOGY_PAIR_TTL` (30sn) bekler. Yavaş bir
bağlantı üzerinden bu süreyi artırın.

**Bir servis keşfedilen uygulamalar listesinde yok.**
Liste gerçekten alınan telemetriden geliyor. Servis saklama penceresi içinde
span göndermediyse listede olmaz.

**Kod seviyesine aldığım servis span üretmiyor.**
Ya arayüz yerine sınıf olarak kayıtlı (açılış logundaki atlananlar listesine
bakın) ya da `AddNabizCodeLevel()` kayıttan sonra değil önce çağrılmış.

**Dump "connection refused" ile başarısız.**
nabiz uygulamanın tanılama ucuna ulaşamadı. Uygulama kapalı olabilir, port
nabiz'den erişilebilir olmayabilir ya da araya NAT/vekil girmiş olabilir — son
durumda `advertisedHost` ayarlayın. Arayüz ham ağ hatasını anlaşılır bir mesaja
çeviriyor; yeniden denemek yerine onu okuyun.

**Tanılama jetonu kaydedilmiyor.**
`NABIZ_SECRET_KEY` tanımlı değil. Anahtar yokken jetonlar düz metin saklanmak
yerine bilerek hiç saklanmıyor.

**Örnek listesinde bir şey "eski" görünüyor.**
Agent'lar 60 saniyede bir yeniden kayıt olur. Eski, nabiz'in ondan haber
almadığı anlamına gelir; genelde süreç kapanmıştır.
