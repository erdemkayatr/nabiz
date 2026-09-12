using Nabiz.Agent;
using Nabiz.Agent.Diagnostics;
using Npgsql;
using Shop.Servisler;

// Bu dosyada agent'ı başlatan tek satır yok: Nabiz.Agent paketi derleme
// sırasında bir [ModuleInitializer] enjekte ediyor ve agent uygulama
// açılırken kendiliğinden devreye giriyor.

var builder = WebApplication.CreateBuilder(args);
builder.Services.AddHttpClient();

// Gerçek bir veritabanı ve gerçek bir dış servis.
builder.Services.AddSingleton(new NpgsqlDataSourceBuilder(
    "Host=localhost;Username=nabiz;Password=nabiz;Database=orders").Build());
builder.Services.AddHttpClient("katalog", c => c.BaseAddress = new Uri("http://localhost:8081"));

builder.Services.AddScoped<ISepetServisi, SepetServisi>();
builder.Services.AddScoped<IFiyatServisi, FiyatServisi>();
builder.Services.AddScoped<IStokServisi, StokServisi>();
builder.Services.AddScoped<IKatalogServisi, KatalogServisi>();

// Tek satır. Yukarıdaki üç servisin BÜTÜN metotları, kodlarına
// dokunulmadan ölçülmeye başlar.
builder.Services.AddNabizCodeLevel();

// Tanılama uçları. nabiz.json'daki diagnostics bölümü kapalıysa hiçbir uç
// açılmaz; jeton verilmeden de açılmaz.
builder.Services.AddNabizDiagnostics();

var app = builder.Build();

app.MapGet("/", () => "nabiz agent örneği");

// Bu uçta NabizTracer kullanılmıyor: tüm kırılım DI sarmalamasından geliyor.
app.MapGet("/siparis", async (ISepetServisi sepet, int? adet) =>
    Results.Ok(await sepet.SiparisOzetiAsync(adet ?? 3)));

// Otomatik enstrümantasyon bu isteği görür ama içindeki 3 aşamanın
// hangisinin yavaş olduğunu söyleyemez. NabizTracer ile her aşama kendi
// span'ini alır; dosya ve satır bilgisi derleyiciden gelir.
app.MapGet("/hesapla", (int? adet) =>
{
    var sayi = adet ?? 40;

    var sepet = NabizTracer.Measure("sepeti doğrula", () =>
    {
        Thread.Sleep(12);
        return sayi;
    });

    var fiyat = NabizTracer.Measure("fiyat hesapla", () =>
    {
        using var inner = NabizTracer.Start("kampanya uygula");
        Thread.Sleep(45);
        inner.SetTag("kampanya.kod", "YAZ25");
        return sepet * 199.90m;
    });

    NabizTracer.Measure("stok rezerve et", () => Thread.Sleep(8));

    return Results.Ok(new { adet = sepet, tutar = fiyat });
});

// Hatalı uç: istisna yığın iziyle span'e işlenir, kod konumu ayıklanır.
app.MapGet("/patlat", () =>
{
    using var span = NabizTracer.Start("riskli işlem");
    try
    {
        BozukHesap();
        return Results.Ok();
    }
    catch (Exception ex)
    {
        span.Fail(ex);
        return Results.Problem(ex.Message, statusCode: 500);
    }
});

app.Run();

static void BozukHesap()
{
    var bolen = 0;
    _ = 100 / bolen;
}
