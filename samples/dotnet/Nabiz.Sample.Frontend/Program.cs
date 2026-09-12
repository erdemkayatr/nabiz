// Frontend de telemetri kodu içermez. HttpClient çağrıları auto-instrumentation
// tarafından sarılır; trace context (traceparent başlığı) backend'e otomatik
// taşınır. Topolojinin frontend -> backend kenarını çıkarabilmesi buna dayanır.

var builder = WebApplication.CreateBuilder(args);

var backendUrl = builder.Configuration["Backend:Url"] ?? "http://backend:8080";
builder.Services.AddHttpClient("backend", client =>
{
    client.BaseAddress = new Uri(backendUrl);
    client.Timeout = TimeSpan.FromSeconds(10);
});

var app = builder.Build();

app.MapGet("/health", () => Results.Ok("ok"));

app.MapGet("/shop", async (IHttpClientFactory factory) =>
{
    var client = factory.CreateClient("backend");
    var orders = await client.GetFromJsonAsync<List<Order>>("/orders?limit=10");
    return Results.Ok(new { source = "frontend", count = orders?.Count ?? 0, orders });
});

app.MapGet("/shop/orders/{id:int}", async (IHttpClientFactory factory, int id) =>
{
    var client = factory.CreateClient("backend");
    var response = await client.GetAsync($"/orders/{id}");
    return Results.Content(
        await response.Content.ReadAsStringAsync(),
        "application/json",
        statusCode: (int)response.StatusCode);
});

app.MapGet("/shop/checkout", async (IHttpClientFactory factory) =>
{
    var client = factory.CreateClient("backend");
    var response = await client.GetAsync("/orders/flaky");
    return Results.Content(
        await response.Content.ReadAsStringAsync(),
        "application/json",
        statusCode: (int)response.StatusCode);
});

app.MapGet("/shop/report", async (IHttpClientFactory factory) =>
{
    var client = factory.CreateClient("backend");
    var response = await client.GetAsync("/reports/daily");
    return Results.Content(
        await response.Content.ReadAsStringAsync(),
        "application/json",
        statusCode: (int)response.StatusCode);
});

app.Run();

internal record Order(int Id, string Customer, int TotalCents, string Status);
