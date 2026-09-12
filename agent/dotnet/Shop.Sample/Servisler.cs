using Nabiz.Agent;
using Npgsql;

namespace Shop.Servisler;

// DİKKAT: Bu dosyada tek bir telemetri satırı, tek bir attribute yok.
// Metotlar AddNabizCodeLevel() sayesinde otomatik ölçülüyor; içlerindeki
// veritabanı sorguları ve HTTP çağrıları da otomatik enstrümantasyondan
// geliyor.

public interface ISepetServisi
{
    Task<object> SiparisOzetiAsync(int adet);
}

public interface IFiyatServisi
{
    decimal BirimFiyat(string urunKodu);
    Task<decimal> KampanyaUygulaAsync(decimal tutar);
    bool GecerliAdet(int adet);
}

public interface IStokServisi
{
    Task<int> StokSayisiAsync();
}

public interface IKatalogServisi
{
    Task<int> DisServistenUrunSayisiAsync();
}

public sealed class SepetServisi(
    IFiyatServisi fiyat, IStokServisi stok, IKatalogServisi katalog) : ISepetServisi
{
    public async Task<object> SiparisOzetiAsync(int adet)
    {
        for (var i = 0; i < 50; i++) fiyat.GecerliAdet(adet);   // ölçülmemeli
        var birim = fiyat.BirimFiyat("URN-1");
        var tutar = await fiyat.KampanyaUygulaAsync(birim * adet);
        var mevcut = await stok.StokSayisiAsync();
        var katalogAdedi = await katalog.DisServistenUrunSayisiAsync();
        return new { adet, tutar, mevcut, katalogAdedi };
    }
}

public sealed class FiyatServisi : IFiyatServisi
{
    // Her istekte defalarca çağrılan minik bir kontrol: ölçüm maliyeti işin
    // kendisinden büyük olurdu.
    [NabizIgnore]
    public bool GecerliAdet(int adet) => adet > 0 && adet < 1000;

    [NabizTrace(Name = "birim fiyat oku")]
    public decimal BirimFiyat(string urunKodu)
    {
        Thread.Sleep(11);
        return 199.90m;
    }

    public async Task<decimal> KampanyaUygulaAsync(decimal tutar)
    {
        await Task.Delay(24);
        var gecici = new byte[512 * 1024];
        gecici[0] = 1;
        return tutar * 0.85m;
    }
}

/// <summary>Gerçek bir Postgres sorgusu atar.</summary>
public sealed class StokServisi(NpgsqlDataSource db) : IStokServisi
{
    public async Task<int> StokSayisiAsync()
    {
        await using var cmd = db.CreateCommand(
            "SELECT count(*), pg_sleep(0.05) FROM orders WHERE status = 'pending'");
        await using var reader = await cmd.ExecuteReaderAsync();
        await reader.ReadAsync();
        return (int)reader.GetInt64(0);
    }
}

/// <summary>Başka bir servise (sample-backend) HTTP çağrısı yapar.</summary>
public sealed class KatalogServisi(IHttpClientFactory factory) : IKatalogServisi
{
    public async Task<int> DisServistenUrunSayisiAsync()
    {
        var client = factory.CreateClient("katalog");
        var liste = await client.GetFromJsonAsync<List<object>>("/orders?limit=25");
        return liste?.Count ?? 0;
    }
}
