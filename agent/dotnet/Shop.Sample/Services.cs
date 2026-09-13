using Nabiz.Agent;
using Npgsql;

namespace Shop.Services;

// NOTE: there is not a single line of telemetry code, and not one attribute,
// in this file for that purpose. The methods are measured automatically thanks
// to AddNabizCodeLevel(); the database queries and HTTP calls inside them come
// from automatic instrumentation.

public interface ICartService
{
    Task<object> OrderSummaryAsync(int quantity);
}

public interface IPricingService
{
    decimal UnitPrice(string productCode);
    Task<decimal> ApplyCampaignAsync(decimal amount);
    bool IsValidQuantity(int quantity);
}

public interface IStockService
{
    Task<int> StockCountAsync();
}

public interface ICatalogService
{
    Task<int> ProductCountFromExternalServiceAsync();
}

public sealed class CartService(
    IPricingService pricing, IStockService stock, ICatalogService catalog) : ICartService
{
    public async Task<object> OrderSummaryAsync(int quantity)
    {
        for (var i = 0; i < 50; i++) pricing.IsValidQuantity(quantity);   // must not be measured
        var unit = pricing.UnitPrice("SKU-1");
        var amount = await pricing.ApplyCampaignAsync(unit * quantity);
        var available = await stock.StockCountAsync();
        var catalogCount = await catalog.ProductCountFromExternalServiceAsync();
        return new { quantity, amount, available, catalogCount };
    }
}

public sealed class PricingService : IPricingService
{
    // A tiny check called many times per request: measuring it would cost more
    // than the work itself.
    [NabizIgnore]
    public bool IsValidQuantity(int quantity) => quantity > 0 && quantity < 1000;

    [NabizTrace(Name = "read unit price")]
    public decimal UnitPrice(string productCode)
    {
        Thread.Sleep(11);
        return 199.90m;
    }

    public async Task<decimal> ApplyCampaignAsync(decimal amount)
    {
        await Task.Delay(24);
        var scratch = new byte[512 * 1024];
        scratch[0] = 1;
        return amount * 0.85m;
    }
}

/// <summary>Issues a real Postgres query.</summary>
public sealed class StockService(NpgsqlDataSource db) : IStockService
{
    public async Task<int> StockCountAsync()
    {
        await using var cmd = db.CreateCommand(
            "SELECT count(*), pg_sleep(0.05) FROM orders WHERE status = 'pending'");
        await using var reader = await cmd.ExecuteReaderAsync();
        await reader.ReadAsync();
        return (int)reader.GetInt64(0);
    }
}

/// <summary>Makes an HTTP call to another service (sample-backend).</summary>
public sealed class CatalogService(IHttpClientFactory factory) : ICatalogService
{
    public async Task<int> ProductCountFromExternalServiceAsync()
    {
        var client = factory.CreateClient("catalog");
        var list = await client.GetFromJsonAsync<List<object>>("/orders?limit=25");
        return list?.Count ?? 0;
    }
}
