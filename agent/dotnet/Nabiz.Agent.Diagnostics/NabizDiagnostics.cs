using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Http;
using Microsoft.Diagnostics.NETCore.Client;
using Microsoft.Extensions.DependencyInjection;
using Nabiz.Agent;

namespace Nabiz.Agent.Diagnostics;

/// <summary>
/// Talep üzerine CPU profili ve bellek dump'ı alan uçları uygulamaya ekler.
/// </summary>
/// <remarks>
/// <para>
/// <b>Bellek dump'ı sürecin tüm belleğini diske yazar:</b> bağlantı dizeleri,
/// oturum jetonları, parolalar, müşteri verisi. Bu yüzden özellik varsayılan
/// olarak kapalıdır ve jeton verilmeden açılmaz.
/// </para>
/// <para>
/// Kurulum tek satır; gerisi nabiz.json'daki <c>diagnostics</c> bölümünden
/// yönetilir:
/// </para>
/// <code>
/// builder.Services.AddNabizDiagnostics();
/// </code>
/// </remarks>
public static class NabizDiagnosticsExtensions
{
    /// <summary>Tanılama uçlarını kaydeder.</summary>
    public static IServiceCollection AddNabizDiagnostics(
        this IServiceCollection services,
        Action<NabizOptions.DiagnosticsSettings>? configure = null)
    {
        var settings = NabizAgent.Options?.Diagnostics ?? new NabizOptions.DiagnosticsSettings();
        configure?.Invoke(settings);

        if (!settings.Enabled)
        {
            Console.WriteLine("[nabiz] tanılama uçları kapalı (diagnostics.enabled = false)");
            return services;
        }

        // Jetonsuz açılmaz. Yanlışlıkla enabled=true bırakılmış bir
        // yapılandırma, süreç belleğini isteyen herkese açık hale getirirdi.
        if (string.IsNullOrWhiteSpace(settings.Token) || settings.Token.Length < 16)
        {
            Console.Error.WriteLine(
                "[nabiz] tanılama uçları AÇILMADI: diagnostics.token en az 16 karakter olmalı. " +
                "Bellek dump'ı bağlantı dizesi ve jeton içerir; jetonsuz açılamaz.");
            return services;
        }

        var store = new DumpStore(settings);
        services.AddSingleton(store);
        services.AddSingleton(settings);
        services.AddSingleton<Microsoft.AspNetCore.Hosting.IStartupFilter>(
            new DiagnosticsStartupFilter(settings, store));

        // nabiz arayüzünden tetiklenebilmesi için kendimizi tanıtıyoruz.
        services.AddHostedService(provider => new AgentRegistration(
            settings, provider.GetRequiredService<Microsoft.AspNetCore.Hosting.Server.IServer>()));

        Console.WriteLine($"[nabiz] tanılama uçları açık: {settings.Path} " +
                          $"(dizin: {store.Directory_}, indirme: {(settings.AllowDownload ? "açık" : "kapalı")})");
        return services;
    }
}

/// <summary>
/// Tanılama ara katmanını boru hattının en başına yerleştirir.
/// </summary>
/// <remarks>
/// IStartupFilter kullanılıyor ki uygulamanın <c>Program.cs</c>'inde ayrıca
/// <c>app.Map...</c> çağrısı gerekmesin. Uçlar yönlendirmeden önce çalışır:
/// uygulamanın kendi boru hattı bozulmuşken de dump alınabilmeli.
/// </remarks>
internal sealed class DiagnosticsStartupFilter(
    NabizOptions.DiagnosticsSettings settings, DumpStore store) : IStartupFilter
{
    public Action<IApplicationBuilder> Configure(Action<IApplicationBuilder> next) => app =>
    {
        app.Use(async (context, nextMiddleware) =>
        {
            if (!context.Request.Path.StartsWithSegments(settings.Path, out var remaining))
            {
                await nextMiddleware().ConfigureAwait(false);
                return;
            }
            await DiagnosticsEndpoints.HandleAsync(context, remaining, settings, store).ConfigureAwait(false);
        });
        next(app);
    };
}

/// <summary>Tanılama uçlarının gövdesi.</summary>
internal static class DiagnosticsEndpoints
{
    private static readonly JsonSerializerOptions Json = new() { WriteIndented = true };

    public static async Task HandleAsync(
        HttpContext context, PathString remaining,
        NabizOptions.DiagnosticsSettings settings, DumpStore store)
    {
        if (!IsAuthorized(context, settings))
        {
            // 404 değil 401: yolun varlığını zaten jeton sahibi biliyor,
            // yanlış jetonla gelen de net bir cevap almalı.
            context.Response.StatusCode = StatusCodes.Status401Unauthorized;
            await WriteAsync(context, new { error = "X-Nabiz-Token geçersiz" }).ConfigureAwait(false);
            return;
        }

        var segment = remaining.Value?.Trim('/') ?? "";
        var method = context.Request.Method;

        try
        {
            switch (method, segment)
            {
                case ("POST", "cpu"):
                {
                    var seconds = ReadInt(context, "seconds", 20);
                    var file = await store.CaptureCpuAsync(seconds, context.RequestAborted).ConfigureAwait(false);
                    Log(context, $"CPU profili {seconds}sn");
                    await WriteAsync(context, Describe(file, settings)).ConfigureAwait(false);
                    return;
                }
                case ("POST", "memory"):
                {
                    var type = ReadDumpType(context);
                    var file = await store.CaptureMemoryAsync(type, context.RequestAborted).ConfigureAwait(false);
                    Log(context, $"bellek dump'ı ({type})");
                    await WriteAsync(context, Describe(file, settings)).ConfigureAwait(false);
                    return;
                }
                case ("GET", ""):
                    await WriteAsync(context, new
                    {
                        directory = store.Directory_,
                        downloadEnabled = settings.AllowDownload,
                        files = store.List().Select(f => Describe(f, settings)),
                    }).ConfigureAwait(false);
                    return;

                case ("GET", _) when segment.Length > 0:
                {
                    if (!settings.AllowDownload)
                    {
                        context.Response.StatusCode = StatusCodes.Status403Forbidden;
                        await WriteAsync(context, new
                        {
                            error = "indirme kapalı (diagnostics.allowDownload = false)",
                            directory = store.Directory_,
                        }).ConfigureAwait(false);
                        return;
                    }
                    var file = store.Find(segment);
                    if (file is null) { context.Response.StatusCode = StatusCodes.Status404NotFound; return; }

                    Log(context, $"indirme {file.Id} ({file.Bytes / 1024 / 1024} MB)");
                    context.Response.ContentType = "application/octet-stream";
                    context.Response.Headers.ContentDisposition = $"attachment; filename=\"{file.Id}\"";
                    await context.Response.SendFileAsync(file.Path, context.RequestAborted).ConfigureAwait(false);
                    return;
                }

                case ("DELETE", _) when segment.Length > 0:
                    context.Response.StatusCode = store.Delete(segment)
                        ? StatusCodes.Status204NoContent
                        : StatusCodes.Status404NotFound;
                    return;

                default:
                    context.Response.StatusCode = StatusCodes.Status404NotFound;
                    await WriteAsync(context, new
                    {
                        error = "bilinmeyen uç",
                        endpoints = new[]
                        {
                            $"POST {settings.Path}/cpu?seconds=20",
                            $"POST {settings.Path}/memory?type=heap|full|mini|triage",
                            $"GET {settings.Path}",
                            $"GET {settings.Path}/{{dosya}}",
                            $"DELETE {settings.Path}/{{dosya}}",
                        },
                    }).ConfigureAwait(false);
                    return;
            }
        }
        catch (Exception ex)
        {
            // Tanılama ucunun hatası uygulamayı etkilememeli.
            context.Response.StatusCode = StatusCodes.Status500InternalServerError;
            await WriteAsync(context, new { error = ex.Message, type = ex.GetType().Name }).ConfigureAwait(false);
        }
    }

    /// <summary>Jetonu sabit zamanlı karşılaştırır.</summary>
    private static bool IsAuthorized(HttpContext context, NabizOptions.DiagnosticsSettings settings)
    {
        var provided = context.Request.Headers["X-Nabiz-Token"].ToString();
        if (string.IsNullOrEmpty(provided)) return false;

        var a = Encoding.UTF8.GetBytes(provided);
        var b = Encoding.UTF8.GetBytes(settings.Token);
        // FixedTimeEquals eşit uzunluk ister; uzunluk farkı da sızdırmasın
        // diye önce sabit boyuta özetliyoruz.
        return CryptographicOperations.FixedTimeEquals(SHA256.HashData(a), SHA256.HashData(b));
    }

    private static object Describe(DumpFile file, NabizOptions.DiagnosticsSettings settings) => new
    {
        id = file.Id,
        kind = file.Kind,
        bytes = file.Bytes,
        megabytes = Math.Round(file.Bytes / 1024d / 1024d, 1),
        createdAt = file.CreatedAt,
        path = file.Path,
        downloadUrl = settings.AllowDownload ? $"{settings.Path}/{file.Id}" : null,
    };

    private static int ReadInt(HttpContext context, string name, int fallback) =>
        int.TryParse(context.Request.Query[name], out var value) ? value : fallback;

    private static DumpType ReadDumpType(HttpContext context) =>
        context.Request.Query["type"].ToString().ToLowerInvariant() switch
        {
            "full" => DumpType.Full,
            "mini" => DumpType.Normal,
            "triage" => DumpType.Triage,
            _ => DumpType.WithHeap,   // varsayılan: heap'i içerir ama Full kadar büyük değil
        };

    // Dump alma işlemi izi kalmalı: kim, ne zaman, neyi aldı.
    private static void Log(HttpContext context, string what) =>
        Console.WriteLine($"[nabiz] tanılama: {what} — istemci {context.Connection.RemoteIpAddress}");

    private static Task WriteAsync(HttpContext context, object payload)
    {
        context.Response.ContentType = "application/json; charset=utf-8";
        return context.Response.WriteAsync(JsonSerializer.Serialize(payload, Json));
    }
}
