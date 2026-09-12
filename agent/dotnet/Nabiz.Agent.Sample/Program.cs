using Nabiz.Agent;

// Bu dosyada agent'ı başlatan tek satır yok: Nabiz.Agent paketi derleme
// sırasında bir [ModuleInitializer] enjekte ediyor ve agent uygulama
// açılırken kendiliğinden devreye giriyor.

var builder = WebApplication.CreateBuilder(args);
builder.Services.AddHttpClient();
var app = builder.Build();

app.MapGet("/", () => "nabiz agent örneği");

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
