using Nabiz.Agent;
using Nabiz.Agent.Diagnostics;
using Npgsql;
using Shop.Services;

// There is no line in this file that starts the agent: at build time the
// Nabiz.Agent package injects a [ModuleInitializer], and the agent comes up by
// itself when the application starts.

var builder = WebApplication.CreateBuilder(args);
builder.Services.AddHttpClient();

// A real database and a real external service.
builder.Services.AddSingleton(new NpgsqlDataSourceBuilder(
    "Host=localhost;Username=nabiz;Password=nabiz;Database=orders").Build());
builder.Services.AddHttpClient("catalog", c => c.BaseAddress = new Uri("http://localhost:8081"));

builder.Services.AddScoped<ICartService, CartService>();
builder.Services.AddScoped<IPricingService, PricingService>();
builder.Services.AddScoped<IStockService, StockService>();
builder.Services.AddScoped<ICatalogService, CatalogService>();

// One line. EVERY method of the services above starts being measured, with no
// change to their code.
builder.Services.AddNabizCodeLevel();

// The diagnostics endpoints. If the diagnostics section of nabiz.json is off,
// no endpoint opens at all; and none opens without a token either.
builder.Services.AddNabizDiagnostics();

var app = builder.Build();

app.MapGet("/", () => "nabiz agent sample");

// This endpoint does not use NabizTracer: the whole breakdown comes from the
// DI wrapping.
app.MapGet("/order", async (ICartService cart, int? quantity) =>
    Results.Ok(await cart.OrderSummaryAsync(quantity ?? 3)));

// Automatic instrumentation sees this request but cannot say which of the
// three stages inside it is slow. With NabizTracer each stage gets its own
// span, and the file and line come from the compiler.
app.MapGet("/calculate", (int? quantity) =>
{
    var count = quantity ?? 40;

    var validated = NabizTracer.Measure("validate cart", () =>
    {
        Thread.Sleep(12);
        return count;
    });

    var price = NabizTracer.Measure("calculate price", () =>
    {
        using var inner = NabizTracer.Start("apply campaign");
        Thread.Sleep(45);
        inner.SetTag("campaign.code", "SUMMER25");
        return validated * 199.90m;
    });

    NabizTracer.Measure("reserve stock", () => Thread.Sleep(8));

    return Results.Ok(new { quantity = validated, amount = price });
});

// A failing endpoint: the exception is recorded on the span with its stack
// trace, and the code location is extracted from it.
app.MapGet("/boom", () =>
{
    using var span = NabizTracer.Start("risky operation");
    try
    {
        BrokenCalculation();
        return Results.Ok();
    }
    catch (Exception ex)
    {
        span.Fail(ex);
        return Results.Problem(ex.Message, statusCode: 500);
    }
});

app.Run();

static void BrokenCalculation()
{
    var divisor = 0;
    _ = 100 / divisor;
}
