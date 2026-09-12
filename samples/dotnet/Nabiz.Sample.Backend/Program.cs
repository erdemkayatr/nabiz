using Npgsql;

// Bu dosyada tek satır telemetri kodu yok. nabiz'in .NET desteği, OpenTelemetry
// auto-instrumentation'ın CLR Profiler API'si üzerinden çalışır: uygulama
// derlenirken değil, başlatılırken devreye girer.

var builder = WebApplication.CreateBuilder(args);

var connectionString = builder.Configuration.GetConnectionString("Orders")
    ?? "Host=postgres;Username=nabiz;Password=nabiz;Database=orders";

var dataSource = new NpgsqlDataSourceBuilder(connectionString).Build();
builder.Services.AddSingleton(dataSource);

var app = builder.Build();

await EnsureSchemaAsync(dataSource);

app.MapGet("/health", () => Results.Ok("ok"));

app.MapGet("/orders", async (NpgsqlDataSource db, int? limit) =>
{
    await using var cmd = db.CreateCommand(
        "SELECT id, customer, total_cents, status FROM orders ORDER BY id DESC LIMIT $1");
    cmd.Parameters.AddWithValue(Math.Clamp(limit ?? 20, 1, 200));

    var orders = new List<object>();
    await using var reader = await cmd.ExecuteReaderAsync();
    while (await reader.ReadAsync())
    {
        orders.Add(new
        {
            id = reader.GetInt32(0),
            customer = reader.GetString(1),
            totalCents = reader.GetInt32(2),
            status = reader.GetString(3),
        });
    }
    return Results.Ok(orders);
});

app.MapGet("/orders/{id:int}", async (NpgsqlDataSource db, int id) =>
{
    await using var cmd = db.CreateCommand(
        "SELECT id, customer, total_cents, status FROM orders WHERE id = $1");
    cmd.Parameters.AddWithValue(id);

    await using var reader = await cmd.ExecuteReaderAsync();
    if (!await reader.ReadAsync())
    {
        return Results.NotFound(new { error = "sipariş bulunamadı", id });
    }
    return Results.Ok(new
    {
        id = reader.GetInt32(0),
        customer = reader.GetString(1),
        totalCents = reader.GetInt32(2),
        status = reader.GetString(3),
    });
});

// Hata oranı ve yavaş uç: topolojide kırmızı kenar ve gecikme dağılımı
// görebilmek için kasıtlı.
app.MapGet("/orders/flaky", () =>
    Random.Shared.Next(100) < 15
        ? Results.Problem("aşağı akış ödeme sağlayıcısı yanıt vermedi", statusCode: 503)
        : Results.Ok(new { status = "ok" }));

app.MapGet("/reports/daily", async (NpgsqlDataSource db) =>
{
    // pg_sleep ile bilerek yavaş bir sorgu: p99 kuyruğunu doldurur.
    await using var cmd = db.CreateCommand(
        "SELECT count(*), pg_sleep(0.15) FROM orders");
    await using var reader = await cmd.ExecuteReaderAsync();
    await reader.ReadAsync();
    return Results.Ok(new { orders = reader.GetInt64(0) });
});

app.Run();

static async Task EnsureSchemaAsync(NpgsqlDataSource db)
{
    for (var attempt = 1; attempt <= 30; attempt++)
    {
        try
        {
            await using var create = db.CreateCommand("""
                CREATE TABLE IF NOT EXISTS orders (
                    id           SERIAL PRIMARY KEY,
                    customer     TEXT NOT NULL,
                    total_cents  INT  NOT NULL,
                    status       TEXT NOT NULL
                );
                """);
            await create.ExecuteNonQueryAsync();

            await using var seed = db.CreateCommand("""
                INSERT INTO orders (customer, total_cents, status)
                SELECT 'musteri-' || g, (random() * 50000)::int, 
                       (ARRAY['pending','shipped','delivered'])[1 + (random() * 2)::int]
                FROM generate_series(1, 500) g
                WHERE NOT EXISTS (SELECT 1 FROM orders);
                """);
            await seed.ExecuteNonQueryAsync();
            return;
        }
        catch (NpgsqlException) when (attempt < 30)
        {
            // Postgres henüz ayakta değil.
            await Task.Delay(TimeSpan.FromSeconds(2));
        }
    }
}
