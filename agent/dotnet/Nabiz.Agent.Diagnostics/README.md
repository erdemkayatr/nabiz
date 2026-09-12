# Nabiz.Agent.Diagnostics

nabiz agent'ı için talep üzerine **CPU profili** ve **bellek dump'ı**.
Uygulamaya bir istek atarsınız, dosya üretilir.

> **Bellek dump'ı sürecin tüm belleğini diske yazar:** bağlantı dizeleri,
> oturum jetonları, parolalar, müşteri verisi. Bu yüzden özellik varsayılan
> olarak **kapalıdır** ve **jeton verilmeden açılmaz**.

Ayrı bir paket: tanılama IPC yığını yalnızca ihtiyacı olan uygulamalara
girsin.

```bash
dotnet add package Nabiz.Agent.Diagnostics
```

```csharp
builder.Services.AddNabizDiagnostics();
```

`nabiz.json`:

```json
"diagnostics": {
  "enabled": true,
  "path": "/nabiz/diag",
  "token": "en-az-16-karakter-rastgele-bir-deger",
  "outputDirectory": "",
  "maxFiles": 5,
  "maxCpuSeconds": 120,
  "allowDownload": false
}
```

## Uçlar

Hepsi `X-Nabiz-Token` başlığı ister.

| Uç | Ne yapar |
|---|---|
| `POST /nabiz/diag/cpu?seconds=20` | CPU örneklemesi, `.nettrace` üretir |
| `POST /nabiz/diag/memory?type=heap` | Bellek dump'ı, `.dmp` üretir |
| `GET /nabiz/diag` | Üretilen dosyaları listeler |
| `GET /nabiz/diag/{dosya}` | İndirir (`allowDownload` gerekir) |
| `DELETE /nabiz/diag/{dosya}` | Siler |

```bash
curl -X POST -H "X-Nabiz-Token: $TOKEN" \
  "http://localhost:5000/nabiz/diag/cpu?seconds=20"
```

`type` değerleri: `heap` (varsayılan), `full`, `mini`, `triage`.

## Çıktıyı okumak

CPU profili ham **nettrace**'tir; bilerek çözümlenmiyor. PerfView, Visual
Studio ve `dotnet-trace convert` bu biçimi zaten okuyor — kendi
çözümleyicimizi yazmak, hatalarını da üstlenmek olurdu.

```bash
dotnet-trace convert cpu-20260912-221500.nettrace --format speedscope
```

Bellek dump'ı için `dotnet-dump analyze` ya da Visual Studio.

## Güvenlik

- Varsayılan kapalı; `enabled: true` **ve** en az 16 karakterlik bir jeton
  gerekir. Jeton kısaysa uçlar açılmaz ve nedeni loglanır.
- Jeton sabit zamanlı karşılaştırılır.
- `allowDownload` ayrı bir anahtardır. Açmak, jetonu ele geçiren birinin
  süreç belleğini HTTP üzerinden indirebilmesi demektir. Kapalıyken dosyalar
  diskte durur; `kubectl cp` ile alınır.
- Her dump isteği istemci adresiyle loglanır.
- Aynı anda tek işlem koşar: iki bellek dump'ı birlikte alınırsa süreç iki kez
  askıya alınır ve zaten sıkıntıda olan bir uygulama büsbütün durur.

## Maliyet

- **CPU profili:** örnekleme süresince ölçülebilir ek yük. Kısa pencereler
  kullanın; `maxCpuSeconds` üst sınırı korur.
- **Bellek dump'ı:** süreç, dump yazılırken **askıya alınır**. Yerel bir
  ölçümde 444 MB'lık bir dump 3.8 saniye sürdü — o süre boyunca uygulama
  istek işlemez. Üretimde trafiği kesilmiş bir örnekte alın.
- Kubernetes'te yazılabilir bir dizin gerekir; `readOnlyRootFilesystem` ile
  çalışan pod'lara bir `emptyDir` bağlayın ve `outputDirectory`'yi oraya
  gösterin.
